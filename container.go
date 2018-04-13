package main

import (
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Sirupsen/logrus"
	"github.com/fsouza/go-dockerclient"
	"github.com/laincloud/networkd/hashmap"
)

type ContainerPortItem struct {
	ip            string
	port          string
	virtualIpPort *VirtualIpPortItem
}
type ContainerItem struct {
	ip           string
	id           string
	status       string
	running      bool
	name         string // name == containerName
	appName      string
	procName     string
	podName      string
	instance     int
	rwlock       sync.RWMutex
	ports        map[string]ContainerPortItem
	modifiedTime int64
}
type ContainerItems map[string][]ContainerItem

type ContainerDb struct {
	db                    *hashmap.HashMap
	latestUpdatedUnixTime int64
}

func (self *Agent) InitContainerDb() {
	self.cDb = &ContainerDb{
		db: hashmap.NewHashMap(),
	}
}

func (self *Agent) FetchContainers() {
	currentUnixTime := time.Now().Unix()
	containers, _ := self.docker.ListContainers(docker.ListContainersOptions{All: false})

	// TODO(xutao) support none lain app container?
	lainletContainers, err := self.ListLainletContainers()
	if err != nil {
		log.WithFields(logrus.Fields{
			"err": err,
		}).Error("Cannot get containers from lainlet")
		return
	}

	for _, container := range containers {
		item := &ContainerItem{
			id:           container.ID,
			modifiedTime: currentUnixTime,
		}
		key := fmt.Sprintf("%s/%s", self.hostname, container.ID)
		cont, ok := lainletContainers[key]
		if !ok {
			log.WithFields(logrus.Fields{
				"key": key,
			}).Debug("Invalid container from lainlet")
			continue
		}
		item.ip = cont.IP
		item.appName = cont.AppName
		// FIXME(xutao) cont.PorcName is cont.PodName, lainlet should be fixed
		dotNames := strings.Split(cont.ProcName, ".")
		item.procName = dotNames[len(dotNames)-1]
		item.instance = cont.InstanceNo
		item.name = fmt.Sprintf("%s.%s", item.appName, item.procName)
		self.cDb.Add(item)
	}
	self.cDb.SetUpdatedUnixTime(currentUnixTime)
}

func (self *Agent) FetchContainersByDocker() {
	currentUnixTime := time.Now().Unix()
	containers, _ := self.docker.ListContainers(docker.ListContainersOptions{All: false})
	for _, container := range containers {
		item := &ContainerItem{
			id:           container.ID,
			modifiedTime: currentUnixTime,
		}
		cont, err := self.docker.InspectContainer(container.ID)
		// TODO(xutao) use continue instead of exit
		if err != nil {
			log.Fatal(err)
		}
		item.ip = cont.NetworkSettings.IPAddress
		item.running = cont.State.Running
		for i := range cont.Config.Env {
			env := cont.Config.Env[i]
			pair := strings.SplitN(env, "=", 2)
			k, v := pair[0], pair[1]
			switch k {
			case "LAIN_APPNAME":
				item.appName = v
			case "LAIN_PROCNAME":
				item.procName = v
			case "DEPLOYD_POD_INSTANCE_NO":
				if number, err := strconv.Atoi(v); err == nil {
					item.instance = number
				}
			case "DEPLOYD_POD_NAME":
				item.podName = v
			default:
			}
			item.name = fmt.Sprintf("%s.%s", item.appName, item.procName)
		}
		// TODO(xutao) support none lain app container?
		if item.appName == "" {
			continue
		}
		self.cDb.Add(item)
	}
	self.cDb.SetUpdatedUnixTime(currentUnixTime)
}

func (self *Agent) GcContainers() {
	items := self.cDb.GetAll()
	currentUnixTime := self.cDb.GetUpdatedUnixTime()
	for _, item := range items {
		if item.modifiedTime < currentUnixTime {
			// TODO(xutao) lock item
			item.running = false
			self.cDb.Remove(item.name)
			log.WithFields(logrus.Fields{
				"containerName": item.name,
			}).Debug("Mark dead container item")
		}
	}
}

func (self *Agent) ListContainers() ContainerItems {
	conts := make(ContainerItems)
	containers, _ := self.docker.ListContainers(docker.ListContainersOptions{All: false})
	for _, container := range containers {
		contConfig := ContainerItem{
			id:     container.ID,
			status: container.Status,
		}
		self.InspectContainer(container.ID[0:12], &contConfig)
		// TODO(xutao) support none lain app container?
		if contConfig.appName == "" {
			continue
		}
		conts[contConfig.name] = append(conts[contConfig.name], contConfig)
	}
	return conts
}

func (self *Agent) InspectContainer(id string, config *ContainerItem) error {
	container, err := self.docker.InspectContainer(id)
	if err != nil {
		return err
	}
	config.ip = container.NetworkSettings.IPAddress
	for i := range container.Config.Env {
		env := container.Config.Env[i]
		pair := strings.SplitN(env, "=", 2)
		k, v := pair[0], pair[1]
		switch k {
		case "LAIN_APPNAME":
			config.appName = v
		case "LAIN_PROCNAME":
			config.procName = v
		case "DEPLOYD_POD_INSTANCE_NO":
			if number, err := strconv.Atoi(v); err == nil {
				config.instance = number
			}
		case "DEPLOYD_POD_NAME":
			config.podName = v
		default:
		}
		config.name = fmt.Sprintf("%s.%s", config.appName, config.procName)
	}
	return err
}

func (self *ContainerDb) Add(item *ContainerItem) {
	// 1. remove old one if exist
	// 2. add new one
	db := self.db
	db.RWLock.Lock()
	defer db.RWLock.Unlock()
	key := item.name
	value := item
	// TODO(xutao) multi-instance
	if oldItem, ok := db.Get(key); ok {
		oldContainerItem := oldItem.(*ContainerItem)

		if oldContainerItem.modifiedTime != item.modifiedTime {
			// new item
			oldContainerItem.running = item.running
			oldContainerItem.modifiedTime = item.modifiedTime
		} else {
			// multi instance
			if oldContainerItem.instance < item.instance {
				value = oldContainerItem
			}
		}

		db.Remove(key)
		log.WithFields(logrus.Fields{
			"containerName": key,
		}).Debug("Remove old container item")
	}
	db.Add(key, value)
	log.WithFields(logrus.Fields{
		"containerName": key,
	}).Debug("Add container item")
}

func (self *ContainerDb) Get(key string) (item *ContainerItem, ok bool) {
	db := self.db
	db.RWLock.RLock()
	defer db.RWLock.RUnlock()
	if item, ok := db.Get(key); ok {
		return item.(*ContainerItem), true
	}
	return
}

func (self *ContainerDb) Remove(key string) {
	db := self.db
	db.RWLock.Lock()
	defer db.RWLock.Unlock()
	db.Remove(key)
}

func (self *ContainerDb) GetAll() []*ContainerItem {
	db := self.db
	db.RWLock.Lock()
	defer db.RWLock.Unlock()
	items := db.Items()
	containerItems := make([]*ContainerItem, 0, len(items))
	for _, value := range items {
		containerItems = append(containerItems, value.(*ContainerItem))
	}
	return containerItems
}

func (self *ContainerDb) SetUpdatedUnixTime(t int64) {
	db := self.db
	db.RWLock.Lock()
	defer db.RWLock.Unlock()
	self.latestUpdatedUnixTime = t
}

func (self *ContainerDb) GetUpdatedUnixTime() int64 {
	db := self.db
	db.RWLock.RLock()
	defer db.RWLock.RUnlock()
	return self.latestUpdatedUnixTime
}
