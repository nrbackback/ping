package main

import (
	"context"
	"encoding/csv"
	"fmt"
	"os"
	"reflect"
	"strconv"
	"sync/atomic"
	"time"

	"github.com/olivere/elastic/v7"
)

func main() {
	ticker := time.NewTicker(pingInterval)
	done := make(chan bool)
	pingWorker := make(map[string]*PingWorker, 0)
	go func() {
		for {
			select {
			case <-done:
				return
			case <-ticker.C:
				ipAddr, size := randIp()
				p := pingWorker[ipAddr]
				if pingWorker[ipAddr] == nil {
					var err error
					p, err = newPingerWorker(ipAddr, size)
					if err != nil {
						panic(err)
					}
					go func() {
						err = p.pinger.Run()
						if err != nil {
							panic(err)
						}
					}()
					pingWorker[ipAddr] = p
				}
				p.pinger.SendPingSignal()
			}
		}
	}()
	// exitChan := make(chan os.Signal)
	// signal.Notify(exitChan, os.Interrupt, syscall.SIGTERM)
	// <-exitChan
	time.Sleep(sleepTime)
	ticker.Stop()
	done <- true
	for _, p := range pingWorker {
		p.pinger.Stop()
	}

	for _, p := range pingWorker {
		p.parseAllLatency()
	}
	writer.Flush()
	fmt.Println("stopped")
}

type PingWorker struct {
	pinger     *Pinger
	dstIP      string
	sendCount  int64
	recvCount  int64
	seqToIndex map[int]int
	pingList   []*PingInfo

	lastTime   *time.Time
	writeCount int64
}

func newPingerWorker(idAddr string, packetSize int) (*PingWorker, error) {
	pinger, err := NewPinger(idAddr)
	if err != nil {
		return nil, err
	}
	if packetSize > 0 {
		pinger.Size = packetSize
	}
	p := &PingWorker{
		pinger:     pinger,
		dstIP:      idAddr,
		seqToIndex: make(map[int]int),
		pingList:   make([]*PingInfo, 0),
	}
	pinger.OnSend = func(pkt *Packet) {
		atomic.AddInt64(&p.sendCount, 1)
		now := time.Now()
		localIP, err := getClientIp()
		if err != nil {
			fmt.Println("getClientIp error", err)
		}
		info := PingInfo{
			Type:     "request",
			IcmpSeq:  pkt.Seq,
			SendTime: &now,
			SrcIP:    localIP,
			DstIP:    pkt.Addr,
		}
		if _, ok := p.seqToIndex[pkt.Seq]; ok {
			fmt.Printf("duplicate seq %d\n", pkt.Seq)
			return
		}
		p.pingList = append(p.pingList, &info)
		p.seqToIndex[pkt.Seq] = len(p.pingList) - 1
	}
	pinger.OnRecv = func(pkt *Packet) {
		if _, ok := p.seqToIndex[pkt.Seq]; !ok {
			fmt.Printf("no ping request for icmp seq %d\n", pkt.Seq)
			return
		}
		atomic.AddInt64(&p.recvCount, 1)
		now := time.Now()
		index := p.seqToIndex[pkt.Seq]
		packet := p.pingList[index]
		packet.RecvTime = &now
		packet.LatencyTime = pkt.Rtt
	}
	pinger.OnFinish = func(stats *Statistics) {
		fmt.Println("finish at ", time.Now(), "sent", stats.PacketsSent, "recv", stats.PacketsRecv,
			"duplicate", stats.PacketsRecvDuplicates, "loss", stats.PacketLoss, "Rtts", len(stats.Rtts))
	}
	return p, nil
}

func (p *PingWorker) parseAllLatency() {
	allPacketInfo := make([]PacketInfo, 0)
	for _, t := range p.pingList {
		var requestLatencyTime time.Duration
		if p.lastTime != nil {
			requestLatencyTime = t.SendTime.Sub(*p.lastTime)
		}
		requestPacket := PacketInfo{
			Type:        "request",
			IcmpSeq:     t.IcmpSeq,
			CreateTime:  *t.SendTime,
			LatencyTime: requestLatencyTime.Seconds(),
			SrcIP:       t.SrcIP,
			DstIP:       t.DstIP,
		}
		allPacketInfo = append(allPacketInfo, requestPacket)
		p.lastTime = t.SendTime
		if t.RecvTime == nil {
			fmt.Printf("response packet loss of icmp seq %d, dst_ip %s\n", t.IcmpSeq, p.dstIP)
			continue
		}
		responsePacket := PacketInfo{
			Type:        "response",
			IcmpSeq:     t.IcmpSeq,
			CreateTime:  *t.RecvTime,
			LatencyTime: t.LatencyTime.Seconds(),
			SrcIP:       t.DstIP,
			DstIP:       t.SrcIP,
		}
		p.lastTime = t.RecvTime
		allPacketInfo = append(allPacketInfo, responsePacket)
	}
	if len(allPacketInfo) == 0 {
		return
	}
	atomic.AddInt64(&p.writeCount, int64(len(allPacketInfo)))
	remoeWrite(allPacketInfo)
	writeToCSV(allPacketInfo)
}

func remoeWrite(allPacketInfo []PacketInfo) {
	client, err := elastic.NewClient(
		elastic.SetURL(esURL),
		elastic.SetSniff(false),
	)
	if err != nil {
		fmt.Printf("new client error %v\n", err)
	}
	batchPacketList := make([]PacketInfo, 0, esBatchSize)
	for _, v := range allPacketInfo {
		batchPacketList = append(batchPacketList, v)
		if len(batchPacketList) == esBatchSize {
			writeToES(client, batchPacketList)
			batchPacketList = make([]PacketInfo, 0, esBatchSize)
		}
	}
	if len(batchPacketList) != 0 {
		writeToES(client, batchPacketList)
	}
}

func writeToES(client *elastic.Client, batchPacketList []PacketInfo) {
	bulkRequest := client.Bulk()
	for _, v := range batchPacketList {
		bulkRequest.Add(elastic.NewBulkIndexRequest().Index(esIndex).Doc(v))
	}
	bulkResponse, err := bulkRequest.Do(context.TODO())
	if err != nil {
		fmt.Printf("bulk request error %v\n", err)
		return
	}
	if bulkResponse == nil {
		fmt.Println("expected bulkResponse to be != nil; got nil")
	}
	if bulkRequest.NumberOfActions() != 0 {
		fmt.Printf("expected bulkRequest.NumberOfActions %d; got %d\n", 0, bulkRequest.NumberOfActions())
	}
}

var writer *csv.Writer

func writeToCSV(allPacketInfo []PacketInfo) {
	if writer == nil {
		_, err := os.Create(csvFile)
		if err != nil {
			fmt.Println(err)
			return
		}
		f, err := os.OpenFile(csvFile, os.O_RDWR, 0666)
		if err != nil {
			fmt.Println("Error: ", err)
			return
		}
		writer = csv.NewWriter(f)
		var tags []string
		st := reflect.TypeOf(allPacketInfo[0])
		for i := 0; i < st.NumField(); i++ {
			tags = append(tags, st.Field(i).Tag.Get("json"))
		}
		writer.Write(tags)
	}

	for _, v := range allPacketInfo {
		writer.Write([]string{v.Type, strconv.FormatInt(int64(v.IcmpSeq), 10),
			v.CreateTime.String(), fmt.Sprintf("%f", v.LatencyTime), v.SrcIP, v.DstIP,
		})
	}
}
