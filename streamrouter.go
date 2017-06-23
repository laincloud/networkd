package main

import (
	"encoding/json"
	"fmt"
	"sync"

	"github.com/Sirupsen/logrus"
	lainlet "github.com/laincloud/lainlet/client"
)

//type JSONVirtualIpConfigs map[string]JSONVirtualIpConfig

const StreamrouterVippoolKey = "streamrouter/vippool"

type StreamRouterConfig struct {
	c    JSONVirtualIpConfig
	vips []string
	lock sync.RWMutex
}

func NewStreamRouterConfig() *StreamRouterConfig {
	return &StreamRouterConfig{
		c: JSONVirtualIpConfig{
			App:           "streamrouter",
			Proc:          "worker",
			Ports:         []JSONVirtualIpPortConfig{},
			ExcludedNodes: []string{},
		},
		vips: make([]string, 1),
	}
}

func (conf *StreamRouterConfig) getConfigValue() string {
	if len(conf.vips) == 0 {
		return ""
	}
	b, err := json.Marshal(conf.c)
	if err != nil {
		log.Error(err)
		return ""
	}
	return string(b)
}

func (conf *StreamRouterConfig) setConfigPorts(ports []int) {
	conf.lock.Lock()
	defer conf.lock.Unlock()
	conf.c.Ports = []JSONVirtualIpPortConfig{}
	for _, v := range ports {
		strPort := fmt.Sprintf("%d", v)
		conf.c.Ports = append(conf.c.Ports, JSONVirtualIpPortConfig{
			Src:  strPort,
			Dest: strPort,
		})
	}
}

func (self *Server) RunStreamrouter() {
	if self.streamrouterIsRunning {
		return
	}
	log.Info("Run streamrouter")
	self.streamrouterIsRunning = true
	stopCh := make(chan struct{})
	conf := NewStreamRouterConfig()
	vipEventCh, portEventCh := self.WatchStreamRouter(conf, stopCh) // vips and ports
	go func() {
		self.wg.Add(1)
		defer func() {
			log.Info("streamRouter Done")
			self.wg.Done()
		}()
		defer close(stopCh)
		for {
			select {
			case <-vipEventCh:
				//TODO VIP is independent, not need to write all keys
				self.SetStreamRouterVips(conf)
			case <-portEventCh:
				self.SetStreamRouterVips(conf)
			case <-self.streamrouterStopCh:
				stopCh <- struct{}{}
				return
			}
		}
	}()
}

func (self *Server) StopStreamrouter() {
	if self.streamrouterIsRunning {
		log.Debug("stop streamrouter")
		self.streamrouterIsRunning = false
		self.streamrouterStopCh <- struct{}{}
	}
}

func (self *Server) WatchStreamRouter(conf *StreamRouterConfig, stopWatchCh <-chan struct{}) (<-chan int, <-chan int) {
	vipEventCh := make(chan int)
	portEventCh := make(chan int)
	watchKey := fmt.Sprintf("/v2/configwatcher?target=%s&heartbeat=5", StreamrouterVippoolKey)
	stopVip := make(chan struct{})
	stopPorts := make(chan struct{})
	go func() {
		select {
		case <-stopWatchCh:
			stopVip <- struct{}{}
			stopPorts <- struct{}{}
		}
	}()
	go self.WatchLainlet(watchKey, stopVip, func(event *lainlet.Response) { //watch vip from lainlet config watcher /lain/config/streamrouter/vippool
		if event.Event == "error" {
			return
		}
		var vipList interface{}
		err := json.Unmarshal(event.Data, &vipList)
		if err != nil {
			log.Error(err)
		}
		if vStrList, ok := vipList.(map[string]interface{})[StreamrouterVippoolKey].(string); ok {
			var vList []string
			err = json.Unmarshal([]byte(vStrList), &vList)
			if err != nil {
				log.Error(err)
			}
			conf.lock.Lock()
			deletedVips := getDeletedVips(conf.vips, vList)
			for _, dvip := range deletedVips {
				self.DeleteLainVip(dvip)
			}
			conf.vips = vList
			//TODO check ip validity
			conf.lock.Unlock()
			vipEventCh <- 1
		} else {
			log.Debug(vipList.(map[string]interface{})[StreamrouterVippoolKey])
		}
	})
	go self.WatchLainlet("/v2/streamrouter/ports", stopPorts, func(event *lainlet.Response) { //watch port from lainlet streamrouter watcher
		log.Debug("stream", event)
		if event.Event == "error" {
			return
		}
		var ports []int
		json.Unmarshal(event.Data, &ports)
		log.Debug("stream:", ports)
		conf.setConfigPorts(ports)
		portEventCh <- 1
	})
	return vipEventCh, portEventCh
}

func getDeletedVips(oldVipList []string, newVipList []string) []string {
	ret := []string{}
	newVipSet := make(map[string]struct{})
	for _, v := range newVipList {
		newVipSet[v] = struct{}{}
	}
	for _, v := range oldVipList {
		if _, ok := newVipSet[v]; !ok {
			ret = append(ret, v)
		}
	}
	return ret
}

func (self *Server) DeleteLainVip(ip string) {
	if ip == "" {
		return
	}
	kv := self.libkv
	key := fmt.Sprintf("%s/%s", EtcdLainVirtualIpKey, ip)
	err := kv.Delete(key)
	if err != nil {
		log.WithFields(logrus.Fields{
			"key": key,
			"err": err,
		}).Error("Cannot delete lain vip")
	}
}

func (self *Server) SetStreamRouterVips(conf *StreamRouterConfig) {
	// etcd key: /lain/config/vips/$vip
	conf.lock.Lock()
	defer conf.lock.Unlock()
	kv := self.libkv
	value := []byte(conf.getConfigValue())
	for _, ip := range conf.vips {
		if ip == "" {
			continue
		}
		key := fmt.Sprintf("%s/%s", EtcdLainVirtualIpKey, ip)
		err := kv.Put(key, value, nil)
		if err != nil {
			log.WithFields(logrus.Fields{
				"key":   key,
				"value": value,
				"err":   err,
			}).Error("Cannot write lain vip")
			continue
		}
	}
}
