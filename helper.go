package main

import (
	"errors"
	"math/rand"
	"net"
	"time"
)

type ipConfig struct {
	IPAddr     string
	PacketSize int
}

var (
	esURL       = "http://127.0.0.1:9200"
	esIndex     = "ping"
	esBatchSize = 50

	ipAndPacketSize = []ipConfig{
		// icmp and (host 127.0.0.1 or host 127.0.0.1)
		{"127.0.0.1", 200},
	}
	pingInterval = time.Second

	csvFile   = "5-0.csv"
	sleepTime = 5 * time.Minute
)

const (
	PingTypeRequest  = "request"
	PingTypeResponse = "response"
)

type PingInfo struct {
	Type        string        `json:"type"`
	IcmpSeq     int           `json:"icmp_seq"`
	SendTime    *time.Time    `json:"send_time"`
	RecvTime    *time.Time    `json:"revc_time"`
	LatencyTime time.Duration `json:"latency_time"`
	SrcIP       string        `json:"src_ip"`
	DstIP       string        `json:"dst_ip"`
}

func getClientIp() (string, error) {
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return "", err
	}
	for _, address := range addrs {
		// 检查ip地址判断是否为回环地址
		if ipnet, ok := address.(*net.IPNet); ok && !ipnet.IP.IsLoopback() {
			if ipnet.IP.To4() != nil {
				return ipnet.IP.String(), nil
			}
		}
	}
	return "", errors.New("Can not find the client ip address!")
}

func randIp() (string, int) {
	id := rand.Intn(len(ipAndPacketSize))
	return ipAndPacketSize[id].IPAddr, ipAndPacketSize[id].PacketSize
}

type PacketInfo struct {
	Type        string    `json:"type"`
	IcmpSeq     int       `json:"icmp_seq"`
	CreateTime  time.Time `json:"create_time"`
	LatencyTime float64   `json:"latency_time"` // 以秒为单位
	SrcIP       string    `json:"src_ip"`
	DstIP       string    `json:"dst_ip"`
}
