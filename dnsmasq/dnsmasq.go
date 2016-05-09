package dnsmasq

import (
	"encoding/json"
	"fmt"
	"github.com/Sirupsen/logrus"
    "github.com/coreos/etcd/Godeps/_workspace/src/golang.org/x/net/context"
	"github.com/docker/libkv/store"
	"io/ioutil"
	lainlet "github.com/laincloud/lainlet/client"
	"os"
	"strconv"
	"strings"
	"syscall"
	"time"
)

const DnsmaqdPidfile = "/var/run/dnsmasq.pid"
const EtcdAddressPrefixKey = "dnsmasq_addresses"
const EtcdServerPrefixKey = "dnsmasq_servers"
const EtcdPrefixKey = "/lain/config"

type AddressItem struct {
	ip     string
	domain string
}

type ServerItem struct {
	ip     string
	port   string
	domain string
}

type Server struct {
	ip             string
	libkv          store.Store
	isRunning      bool
	stopCh         chan struct{}
	eventCh        chan int
	addresses      []AddressItem
	servers        []ServerItem
	lainlet        *lainlet.Client
	log            *logrus.Logger
	hostFilename   string
	serverFilename string
}

type JSONAddressConfig struct {
	Ips  []string `json:"ips"`
	Type string   `json:"type"`
}

type JSONServerConfig struct {
	Servers []string `json:"servers"` // ip#port
}

func New(ip string, kv store.Store, lainlet *lainlet.Client, log *logrus.Logger, host string, server string) *Server {
	return &Server{
		ip:             ip,
		log:            log,
		libkv:          kv,
		lainlet:        lainlet,
		stopCh:         make(chan struct{}),
		eventCh:        make(chan int),
		isRunning:      false,
		hostFilename:   host,
		serverFilename: server,
	}
}

func (self *Server) RunDnsmasqd() {
	self.isRunning = true
	stopAddressCh := make(chan struct{})
	defer close(stopAddressCh)
	stopServerCh := make(chan struct{})
	defer close(stopServerCh)
	go self.WatchDnsmasqAddress(stopAddressCh)
	go self.WatchDnsmasqServer(stopServerCh)
	for {
		select {
		case <-self.eventCh:
			self.log.Debug("Received dnsmasq event")
			self.SaveAddresses()
			self.SaveServers()
			self.RestartDnsmasq()
		case <-self.stopCh:
			self.isRunning = false
			stopAddressCh <- struct{}{}
			stopServerCh <- struct{}{}
			return
		}
	}
}

func (self *Server) StopDnsmasqd() {
	if self.isRunning {
		self.stopCh <- struct{}{}
	}
}

func (self *Server) RestartDnsmasq() {
	content, err := ioutil.ReadFile(DnsmaqdPidfile)
	if err != nil {
		self.log.WithFields(logrus.Fields{
			"filename": DnsmaqdPidfile,
			"err":      err,
		}).Error("Cannot read dnsmasq pidfile")
		return
	}
	pid, err := strconv.ParseInt(
		strings.TrimRight(string(content), "\n"),
		10,
		64,
	)
	if err != nil {
		self.log.WithFields(logrus.Fields{
			"filename": DnsmaqdPidfile,
			"content":  string(content),
			"err":      err,
		}).Error("Cannot parse dnsmasq pidfile")
		return
	}
	process, err := os.FindProcess(int(pid))
	if err != nil {
		self.log.WithFields(logrus.Fields{
			"pid": pid,
			"err": err,
		}).Error("Failed to find dnsmasq process")
		return
	}
	err = process.Signal(syscall.SIGHUP)
	if err != nil {
		self.log.WithFields(logrus.Fields{
			"pid": pid,
			"err": err,
		}).Error("Failed to reload dnsmasq process")
		return
	}
}

func (self *Server) WatchDnsmasqAddress(watchCh <-chan struct{}) {
	keyPrefixLength := len(EtcdAddressPrefixKey) + 1
	url := fmt.Sprintf("/v2/configwatcher?target=%s&heartbeat=5", EtcdAddressPrefixKey)
	retryCounter := 0
	//ctx, _ := context.WithTimeout(context.Background(), time.Second*30)
	//ctx := context.Background()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	for {
		breakWatch := false
		ch, err := self.lainlet.Watch(url, ctx)
		if err != nil {
			self.log.WithFields(logrus.Fields{
				"err":          err,
				"retryCounter": retryCounter,
			}).Error("Fail to connect lainlet")
			if retryCounter > 3 {
				time.Sleep(30 * time.Second)
			} else {
				time.Sleep(1 * time.Second)
			}
			retryCounter++
			continue
		}
		retryCounter = 0
		for {
			select {
			case event, ok := <-ch:
				if !ok {
					breakWatch = true
					break
				}

				if event.Id == 0 {
					// lainlet error for etcd down
					if event.Event == "error" {
						self.log.WithFields(logrus.Fields{
							"id":    event.Id,
							"event": event.Event,
						}).Error("Fail to watch lainlet")
						time.Sleep(5 * time.Second)
					}
					continue
				}
				var addresses []AddressItem
				var addrs interface{}
				err = json.Unmarshal(event.Data, &addrs)
				for key, value := range addrs.(map[string]interface{}) {
					domain := key[keyPrefixLength:]
					self.log.WithFields(logrus.Fields{
						"domain": domain,
						"value":  value.(string),
					}).Debug("Get domain from lainlet")

					var addr JSONAddressConfig
					err = json.Unmarshal([]byte(value.(string)), &addr)
					if err != nil {
						self.log.WithFields(logrus.Fields{
							"key":    fmt.Sprintf("%s/%s/%s", EtcdPrefixKey, EtcdAddressPrefixKey, key),
							"reason": err,
						}).Warn("Cannot parse domain config")
						continue
					}

					self.log.WithFields(logrus.Fields{
						"domain":  domain,
						"address": addr,
					}).Debug("Get domain config from lainlet")

					if addr.Type == "node" {
						// ip = host ip
						ip := self.ip
						item := AddressItem{
							ip:     ip,
							domain: domain,
						}
						addresses = append(addresses, item)
					} else {
						// TODO(xutao) validate ip
						for _, ip := range addr.Ips {
							item := AddressItem{
								ip:     ip,
								domain: domain,
							}
							addresses = append(addresses, item)
						}
					}

					self.addresses = addresses
				}
				self.eventCh <- 1
			case <-watchCh:
				return
			}
			if breakWatch {
				break
			}
		}
		self.log.Error("Fail to watch lainlet")
	}
}

func (self *Server) WatchDnsmasqServer(watchCh <-chan struct{}) {
	keyPrefixLength := len(EtcdServerPrefixKey) + 1
	url := fmt.Sprintf("/v2/configwatcher?target=%s&heartbeat=5", EtcdServerPrefixKey)
	retryCounter := 0
	//ctx, _ := context.WithTimeout(context.Background(), time.Second*30)
	//ctx := context.Background()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	for {
		breakWatch := false
		ch, err := self.lainlet.Watch(url, ctx)
		if err != nil {
			self.log.WithFields(logrus.Fields{
				"err":          err,
				"retryCounter": retryCounter,
			}).Error("Fail to connect lainlet")
			if retryCounter > 3 {
				time.Sleep(30 * time.Second)
			} else {
				time.Sleep(1 * time.Second)
			}
			retryCounter++
			continue
		}
		retryCounter = 0
		for {
			select {
			case event, ok := <-ch:
				if !ok {
					breakWatch = true
					break
				}

				if event.Id == 0 {
					// lainlet error for etcd down
					if event.Event == "error" {
						self.log.WithFields(logrus.Fields{
							"id":    event.Id,
							"event": event.Event,
						}).Error("Fail to watch lainlet")
						time.Sleep(5 * time.Second)
					}
					continue
				}
				var servers []ServerItem
				var addrs interface{}
				err = json.Unmarshal(event.Data, &addrs)
				for key, value := range addrs.(map[string]interface{}) {
					domain := key[keyPrefixLength:]
					self.log.WithFields(logrus.Fields{
						"domain": domain,
						"value":  value.(string),
					}).Debug("Get domain from lainlet")

					var serv JSONServerConfig
					err = json.Unmarshal([]byte(value.(string)), &serv)
					if err != nil {
						self.log.WithFields(logrus.Fields{
							"key":    fmt.Sprintf("/lain/config/%s/%s", EtcdServerPrefixKey, key),
							"reason": err,
						}).Error("Cannot parse domain server config")
						continue
					}

					for _, serverKey := range serv.Servers {
						// TODO(xutao) validate ip
						sharpCount := strings.Count(serverKey, "#")
						if sharpCount == 1 {
							splitKey := strings.SplitN(serverKey, "#", 2)
							ip, port := splitKey[0], splitKey[1]
							item := ServerItem{
								ip:     ip,
								port:   port,
								domain: domain,
							}
							servers = append(servers, item)
						} else {
							self.log.WithFields(logrus.Fields{
								"domain": domain,
								"server": serverKey,
							}).Error("Invalid domain server config")
							continue
						}
					}

					self.log.WithFields(logrus.Fields{
						"domain": domain,
						"server": serv,
					}).Debug("Get domain config from lainlet")

					self.servers = servers
				}
				self.eventCh <- 1
			case <-watchCh:
				return
			}
			if breakWatch {
				break
			}
		}
		self.log.Error("Fail to watch lainlet")
	}
}

func (self *Server) SaveAddresses() {
	data := []byte{}
	for _, addr := range self.addresses {
		content := fmt.Sprintf("%s %s\n", addr.ip, addr.domain)
		data = append(data, content...)
	}
	ioutil.WriteFile(self.hostFilename, data, 0644)
}

func (self *Server) SaveServers() {
	data := []byte{}
	for _, serv := range self.servers {
		content := fmt.Sprintf("server=/%s/%s#%s\n", serv.domain, serv.ip, serv.port)
		data = append(data, content...)
	}
	ioutil.WriteFile(self.serverFilename, data, 0644)
}

func (self *Server) AddAddress(addressDomain string, addressIps []string, addressType string) {
	kv := self.libkv
	key := fmt.Sprintf("%s/%s/%s", EtcdPrefixKey, EtcdAddressPrefixKey, addressDomain)
	data := JSONAddressConfig{
		Ips:  addressIps,
		Type: addressType,
	}
	value, err := json.Marshal(data)
	if err != nil {
		self.log.WithFields(logrus.Fields{
			"key":  key,
			"data": data,
			"err":  err,
		}).Error("Cannot convert address to json")
		return
	}
	// TODO(xutao) retry
	err = kv.Put(key, value, nil)
	if err != nil {
		self.log.WithFields(logrus.Fields{
			"key":   key,
			"value": data,
			"err":   err,
		}).Error("Cannot put dnsmasq address")
		return
	}
}

// servers: [1.1.1.1#53, 2.2.2.2#53]
func (self *Server) AddServer(domain string, servers []string) {
	kv := self.libkv
	key := fmt.Sprintf("%s/%s/%s", EtcdPrefixKey, EtcdServerPrefixKey, domain)
	data := JSONServerConfig{
		Servers: servers,
	}
	value, err := json.Marshal(data)
	if err != nil {
		self.log.WithFields(logrus.Fields{
			"key":  key,
			"data": data,
			"err":  err,
		}).Error("Cannot convert server to json")
		return
	}
	// TODO(xutao) retry
	err = kv.Put(key, value, nil)
	if err != nil {
		self.log.WithFields(logrus.Fields{
			"key":   key,
			"value": data,
			"err":   err,
		}).Error("Cannot put dnsmasq server")
		return
	}
}
