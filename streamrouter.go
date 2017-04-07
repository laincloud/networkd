package main

import (
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/Sirupsen/logrus"
	"github.com/coreos/etcd/Godeps/_workspace/src/golang.org/x/net/context"
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
		defer self.wg.Done()
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
	go func() { //watch vip from lainlet config watcher /lain/config/streamrouter/vippool
		retryCounter := 0
		for {
			ctx := context.Background()
			watchKey := fmt.Sprintf("/v2/configwatcher?target=%s&heartbeat=5", StreamrouterVippoolKey)
			ch, err := self.lainlet.Watch(watchKey, ctx)
			if err != nil {
				log.WithFields(logrus.Fields{
					"err":          err,
					"retryCounter": retryCounter,
				}).Error("Fail to Connect Lainlet")
				if retryCounter > 3 {
					time.Sleep(30 * time.Second)
				} else {
					time.Sleep(1 * time.Second)
				}
				retryCounter++
				continue
			}
			retryCounter = 0
			for event := range ch {
				if event.Id == 0 {
					// lainlet error for etcd down
					if event.Event == "error" {
						log.WithFields(logrus.Fields{
							"id":    event.Id,
							"event": event.Event,
						}).Error("Fail to watch lainlet")
						time.Sleep(5 * time.Second)
					}
					continue
				}
				if event.Event == "error" {
					continue
				}
				var vipList interface{}
				err = json.Unmarshal(event.Data, &vipList)
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
			}
			log.Error("Fail to watch lainlet")
		}
	}()
	go func() { //watch port from lainlet streamrouter watcher

	}()
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
