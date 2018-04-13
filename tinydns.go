package main

import (
	"encoding/json"
	"fmt"
	"github.com/Sirupsen/logrus"
	"strings"
)

const EtcdTinydnsKey = "/lain/config/tinydns_fqdns"

func (self *Agent) RunTinydns() {
	if self.tinydnsIsRunning {
		return
	}
	log.Info("Run tinydns")
	self.tinydnsIsRunning = true
	stopCh := make(chan struct{})
	eventCh := self.WatchTinydnsIps(stopCh)
	go func() {
		self.wg.Add(1)
		defer func() {
			log.Info("tinydns done")
			self.wg.Done()
		}()
		defer close(stopCh)
		for {
			select {
			case <-eventCh:
				self.ApplyTinydnsIps()
			case <-self.tinydnsStopCh:
				stopCh <- struct{}{}
				return
			}
		}
	}()
}

func (self *Agent) StopTinydns() {
	if self.tinydnsIsRunning {
		log.Debug("Stop tinydns")
		self.tinydnsIsRunning = false
		self.tinydnsStopCh <- struct{}{}
	}
}

// TODO(xutao) move to tinydns app
func (self *Agent) WatchTinydnsIps(stopWatchCh <-chan struct{}) <-chan int {
	return self.WatchProcIps(stopWatchCh, "tinydns", "worker")
}

func (self *Agent) ApplyTinydnsIps() {
	kv := self.libkv
	var servers []string
	key := fmt.Sprintf("%s/tinydns/worker", EtcdAppNetworkdKey)
	entries, err := kv.List(key)
	if err != nil {
		log.WithFields(logrus.Fields{
			"key": key,
			"err": err,
		}).Error("Cannot get etcd key")
		return
	}

	for _, pair := range entries {
		log.WithFields(logrus.Fields{
			"key":   pair.Key,
			"value": string(pair.Value),
		}).Debug("Get tinydns key")
		ipKey := pair.Key[len(key)+1:]
		splitKey := strings.SplitN(ipKey, ":", 2)
		ip, port := splitKey[0], splitKey[1]
		servers = append(servers, fmt.Sprintf("%s#%s", ip, port))
	}
	self.godns.AddDomainServer("lain", servers)
	if self.domain != "" {
		self.godns.AddDomainServer(self.domain, servers)
	}
}

func (self *Agent) AddTinydnsDomain(domain string, data []string) {
	kv := self.libkv
	key := fmt.Sprintf("%s/%s", EtcdTinydnsKey, domain)
	value, err := json.Marshal(data)
	if err != nil {
		log.WithFields(logrus.Fields{
			"key":  key,
			"data": data,
			"err":  err,
		}).Error("Cannot convert server to json")
		return
	}
	// TODO(xutao) retry
	err = kv.Put(key, value, nil)
	if err != nil {
		log.WithFields(logrus.Fields{
			"key":   key,
			"value": data,
			"err":   err,
		}).Error("Cannot put tinydns fqdns")
		return
	}
}
