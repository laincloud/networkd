package godns

import (
	"encoding/json"
	"fmt"
	"strings"
	"net"

	"github.com/Sirupsen/logrus"
	"github.com/docker/libkv/store"
	lainlet "github.com/laincloud/lainlet/client"
	"github.com/laincloud/networkd/util"
	"github.com/laincloud/networkd/godns/server"
	"sync"
)

const EtcdGodnsHostsPrefixKey = "domains"
const EtcdGodnsServerPrefixKey = "domain_servers"
const EtcdDomainPrefixKey = "domain"
const EtcdPrefixKey = "/lain/config"

var (
	glog *logrus.Logger
)

type AddressItem struct {
	ips    []string
	domain string
}

type ServerItem struct {
	ip     string
	port   string
	domain string
}

type Godns struct {
	mu sync.RWMutex

	ip            string
	libkv         store.Store
	srv           *server.Server
	isRunning     bool
	stopCh        chan struct{}
	eventCh       chan interface{}

	//hosts will match the domain exactly, e.g. deployd.lain
	hosts         []AddressItem
	domainServers []ServerItem
	//addresses will match all the sub-domains, e.g. *.lain.local
	addresses     []AddressItem
	//staticHosts come from /etc/hosts
	staticHosts   map[string]string

	lainlet       *lainlet.Client
	extra         bool

	webrouterVips  []string
}

type JSONAddressConfig struct {
	Ips  []string `json:"ips"`
	Type string   `json:"type"`
}

type JSONServerConfig struct {
	Servers []string `json:"servers"` // ip#port
}

func New(dnsAddr string, ip string, kv store.Store, lainlet *lainlet.Client, log *logrus.Logger) *Godns {
	glog = log
	srv := server.New(dnsAddr, log)
	godns := &Godns{
		srv:       srv,
		ip:        ip,
		libkv:     kv,
		lainlet:   lainlet,
		stopCh:    make(chan struct{}),
		eventCh:   make(chan interface{}, 10),
		isRunning: false,
	}
	godns.UpdateStaticHosts()
	return godns
}

func (self *Godns) Run() {
	self.isRunning = true
	go self.srv.Run()

	go self.WatchGodnsDomains(self.stopCh)
	go self.WatchDomainServer(self.stopCh)
	go util.WatchFile(StaticHostsFile, self.eventCh, self.stopCh)
	for {
		select {
		case <-self.eventCh:
			glog.Debug("Received dns event")
			removeAllElementsInChan(self.eventCh)
			self.SaveHosts()
			self.SaveDomainServers()
			self.SaveAddresses()
		case <-self.stopCh:
			self.isRunning = false
			return
		}
	}
}

func (self *Godns) Stop() {
	self.srv.Stop()
	if self.isRunning {
		close(self.stopCh)
	}
}

func (self *Godns) DumpConfig() string {
	return self.srv.DumpAllConfig()
}

func (self *Godns) WatchGodnsDomains(watchCh <-chan struct{}) {
	keyPrefixLength := len(EtcdGodnsHostsPrefixKey) + 1
	util.WatchConfig(glog, self.lainlet, EtcdGodnsHostsPrefixKey, watchCh, func(addrs interface{}) {
		var hosts, addresses []AddressItem
		for key, value := range addrs.(map[string]interface{}) {
			domain := key[keyPrefixLength:]
			glog.WithFields(logrus.Fields{
				"domain": domain,
				"value":  value.(string),
			}).Debugf("Get domain from %s", "etcd:/lain/config" + EtcdGodnsHostsPrefixKey)

			var addr JSONAddressConfig
			err := json.Unmarshal([]byte(value.(string)), &addr)
			if err != nil {
				glog.WithFields(logrus.Fields{
					"key":    fmt.Sprintf("%s/%s/%s", EtcdPrefixKey, EtcdGodnsHostsPrefixKey, key),
					"reason": err,
				}).Warn("Cannot parse domain config")
				continue
			}

			glog.WithFields(logrus.Fields{
				"domain":  domain,
				"address": addr,
			}).Debug("Get domain config from lainlet")

			var targetIps []string
			switch addr.Type {
			case "node":
				// ip = host ip
				targetIps = []string{self.ip}
			case "webrouter":
				targetIps = []string{"webrouter"}
			default:
				for _, ip := range addr.Ips {
					if net.ParseIP(ip) != nil {
						targetIps = append(targetIps, ip)
					}
				}
			}
			if len(targetIps) == 0 {
				continue
			}

			item := AddressItem{
				ips:	targetIps,
				domain: domain,
			}
			if strings.HasPrefix(domain, "*.") {
				addresses = append(addresses, item)
			} else {
				hosts = append(hosts, item)
			}
		}
		self.mu.Lock()
		self.hosts = hosts
		self.addresses = addresses
		self.mu.Unlock()
		self.eventCh <- 1
	})
}

func (self *Godns) WatchDomainServer(watchCh <-chan struct{}) {
	keyPrefixLength := len(EtcdGodnsServerPrefixKey) + 1
	util.WatchConfig(glog, self.lainlet, EtcdGodnsServerPrefixKey, watchCh, func(addrs interface{}) {
		var servers []ServerItem
		for key, value := range addrs.(map[string]interface{}) {
			domain := key[keyPrefixLength:]
			glog.WithFields(logrus.Fields{
				"domain": domain,
				"value":  value.(string),
			}).Debug("Get domain from lainlet")

			var serv JSONServerConfig
			err := json.Unmarshal([]byte(value.(string)), &serv)
			if err != nil {
				glog.WithFields(logrus.Fields{
					"key":    fmt.Sprintf("/lain/config/%s/%s", EtcdGodnsServerPrefixKey, key),
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
					glog.WithFields(logrus.Fields{
						"domain": domain,
						"server": serverKey,
					}).Error("Invalid domain server config")
					continue
				}
			}
			glog.WithFields(logrus.Fields{
				"domain": domain,
				"server": serv,
			}).Debug("Get domain config from lainlet")
		}

		self.mu.Lock()
		self.domainServers = servers
		self.mu.Unlock()

		self.eventCh <- 1
	})
}

func (self *Godns) SaveHosts() {
	data := make(map[string][]string)

	self.UpdateStaticHosts()
	self.mu.RLock()
	for domain, ip := range self.staticHosts {
		data[domain] = []string{ip}
	}
	for _, addr := range self.hosts {
		if len(addr.ips) > 0 && addr.ips[0] == "webrouter" {
			data[addr.domain] = self.webrouterVips
		} else {
			data[addr.domain] = addr.ips
		}
	}
	self.mu.RUnlock()

	self.srv.ReplaceHosts(data)
}

func (self *Godns) SaveAddresses() {
	data := make(map[string][]string)
	self.mu.RLock()
	for _, serv := range self.addresses {
		if len(serv.ips) > 0 && serv.ips[0] == "webrouter" {
			data[serv.domain] = self.webrouterVips
		} else {
			data[serv.domain] = serv.ips
		}
	}
	self.mu.RUnlock()

	self.srv.ReplaceAddresses(data)
}

func (self *Godns) SaveDomainServers() {
	data := make(map[string][]string)
	self.mu.RLock()
	for _, serv := range self.domainServers {
		var v []string
		if old, ok := data[serv.domain]; ok {
			v = old
		}
		ip := fmt.Sprintf("%s#%s", serv.ip, serv.port)
		v = append(v, ip)
		data[serv.domain] = v
	}
	self.mu.RUnlock()

	self.srv.ReplaceDomainServers(data)
}

func (self *Godns) AddHost(addressDomain string, addressIps []string, addressType string) {
	kv := self.libkv
	key := fmt.Sprintf("%s/%s/%s", EtcdPrefixKey, EtcdGodnsHostsPrefixKey, addressDomain)
	data := JSONAddressConfig{
		Ips:  addressIps,
		Type: addressType,
	}
	value, err := json.Marshal(data)
	if err != nil {
		glog.WithFields(logrus.Fields{
			"key":  key,
			"data": data,
			"err":  err,
		}).Error("Cannot convert address to json")
		return
	}
	// TODO(xutao) retry
	err = kv.Put(key, value, nil)
	if err != nil {
		glog.WithFields(logrus.Fields{
			"key":   key,
			"value": data,
			"err":   err,
		}).Error("Cannot put godns host")
		return
	}
}

// domainServers: [1.1.1.1#53, 2.2.2.2#53]
func (self *Godns) AddDomainServer(domain string, servers []string) {
	kv := self.libkv
	key := fmt.Sprintf("%s/%s/%s", EtcdPrefixKey, EtcdGodnsServerPrefixKey, domain)
	data := JSONServerConfig{
		Servers: servers,
	}
	value, err := json.Marshal(data)
	if err != nil {
		glog.WithFields(logrus.Fields{
			"key":  key,
			"data": data,
			"err":  err,
		}).Error("Cannot convert server to json")
		return
	}
	// TODO(xutao) retry
	err = kv.Put(key, value, nil)
	if err != nil {
		glog.WithFields(logrus.Fields{
			"key":   key,
			"value": data,
			"err":   err,
		}).Error("Cannot put godns server")
		return
	}
}

func (self *Godns) UpdateWebrouterVips(webrouterVips []string) {
	self.mu.Lock()
	defer self.mu.Unlock()
	self.webrouterVips = webrouterVips
	glog.WithFields(logrus.Fields{
		"vips": webrouterVips,
	}).Info("Update webrouter vips for godns")
	self.eventCh <- 1
}

func removeAllElementsInChan(ch chan interface{}) {
	for {
		select {
		case <-ch:
		default:
			return
		}
	}
}
