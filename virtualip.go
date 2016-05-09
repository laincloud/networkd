package main

import (
	"crypto/md5"
	"fmt"
	"github.com/Sirupsen/logrus"
	"github.com/laincloud/networkd/hashmap"
	"sync"
)

const (
	VirtualIpStateCreated = iota
	VirtualIpStateUsed    = iota
	VirtualIpStateUnused  = iota
	VirtualIpStateDeleted = iota
)

var VirtualIpState = [5]string{
	"created",
	"used",
	"unused",
	"deleted",
}

type VirtualIpPortItem struct {
	port              string
	proto             string
	containerPort     string
	containerIp       string
	containerId       string
	containerStatus   string
	containerInstance int
}

type VirtualIpItem struct {
	ip            string
	port          string // used when key is "IP:PORT"
	appName       string
	procName      string
	containerName string
	rwlock        sync.RWMutex
	ports         map[string]VirtualIpPortItem
	excludedNodes []string
	modifiedTime  int64
	parent        *VirtualIp
	state         int // copied from virtualip
	next          *VirtualIpItem
}

type VirtualIp struct {
	sync.Mutex
	key   string
	state int
}

type VirtualIpDb struct {
	sync.RWMutex
	db                    *hashmap.HashMap
	latestUpdatedUnixTime int64
	ips                   map[string]*VirtualIp
}

func (self *Server) InitVirtualIpDb() {
	self.vDb = &VirtualIpDb{
		db:  hashmap.NewHashMap(),
		ips: make(map[string]*VirtualIp),
	}
}

func (self *Server) AddVirtualIp(ip string, port string, config JSONVirtualIpConfig, currentUnixTime int64) {
	// TODO(xutao) check app & proc name
	item := &VirtualIpItem{
		ip:            ip,
		port:          port,
		appName:       config.App,
		procName:      config.Proc,
		containerName: fmt.Sprintf("%s.%s", config.App, config.Proc),
		ports:         make(map[string]VirtualIpPortItem),
		modifiedTime:  currentUnixTime,
	}
	for _, v := range config.Ports {
		// default proto
		proto := v.Proto
		if proto == "" {
			proto = "tcp"
		}

		// default node port
		nodePort := v.Src
		if nodePort == "" && port != "" {
			nodePort = port
		}

		// default container port
		containerPort := v.Dest
		if containerPort == "" {
			containerPort = nodePort
		}

		port := VirtualIpPortItem{
			port:          nodePort,
			proto:         proto,
			containerPort: containerPort,
		}
		item.ports[v.Src] = port
	}
	item.excludedNodes = config.ExcludedNodes
	self.vDb.Add(item)
}

func (self *Server) ListVirtualIps() <-chan *VirtualIpItem {
	ch := make(chan *VirtualIpItem)
	go func() {
		defer close(ch)
		items := self.vDb.GetAll()
		for _, value := range items {
			ch <- value
		}
	}()
	return ch
}

func (self *Server) GcVirtualIps() {
	// gc virtual ip ports
	// gc virtual ips
	ch := self.ListVirtualIps()
	currentUnixTime := self.vDb.GetUpdatedUnixTime()
	for item := range ch {
		if item.modifiedTime < currentUnixTime {
			vi, _ := self.vDb.GetVirtualIp(item)
			vi.Lock()
			vi.SetState(VirtualIpStateDeleted)
			vi.Unlock()
		}
	}
}

func (self *Server) ApplyVirtualIps() {
	ch := self.ListVirtualIps()
	for item := range ch {
		key := item.GetKey()
		vi, _ := self.vDb.GetVirtualIp(item)
		vi.Lock()
		for _, name := range item.excludedNodes {
			if name == self.hostname {
				vi.SetState(VirtualIpStateDeleted)
				break
			}
		}
		item.state = vi.state
		switch item.state {
		case VirtualIpStateCreated:
			if self.IsValidPod(item) {
				if self.ApplyVirtualIp(item) {
					vi.SetState(VirtualIpStateUsed)
				}
			} else {
				if self.UnApplyVirtualIp(item) {
					vi.SetState(VirtualIpStateUnused)
				}
			}
		case VirtualIpStateUsed:
			if !self.IsValidPod(item) {
				self.UnApplyVirtualIp(item)
				vi.SetState(VirtualIpStateUnused)
			} else {
				self.ApplyVirtualIp(item)
			}
		case VirtualIpStateUnused:
			if self.IsValidPod(item) {
				self.ApplyVirtualIp(item)
				vi.SetState(VirtualIpStateUsed)
			} else {
				self.UnApplyVirtualIp(item)
			}
		case VirtualIpStateDeleted:
			self.UnApplyVirtualIp(item)
			self.vDb.Remove(key)
			vi.SetState(VirtualIpStateCreated)
		default:
		}
		vi.Unlock()
	}
}

func (self *Server) IsValidPod(item *VirtualIpItem) bool {
	var rc = false
	if _, ok := self.cDb.Get(item.containerName); ok {
		rc = true
	}
	return rc
}

func (self *Server) ApplyVirtualIp(item *VirtualIpItem) bool {
	log.WithFields(logrus.Fields{
		"ip":    item.ip,
		"state": item.State(),
	}).Debug("Apply virtual ip")

	state := LockerStateLocked

	if item.port == "" {
		// check locked ip hostname
		state, err := self.LockVirtualIp(item.ip)
		if err != nil {
			log.WithFields(logrus.Fields{
				"ip":  item.ip,
				"err": err,
			}).Error("Cannot lock virtual ip")
			return false
		}

		switch state {
		case LockerStateCreated:
			item.CreateIp(self.iface)
			// arp
			item.BroadcastIp(self.iface)
		case LockerStateLocked:
			item.CreateIp(self.iface)
			state := item.state
			if state == VirtualIpStateCreated || state == VirtualIpStateUnused {
				// arp
				item.BroadcastIp(self.iface)
			}
		case LockerStateUnlocked:
			item.DeleteIp(self.iface)
		case LockerStateError:
			return false
		}
	}

	cont, ok := self.cDb.Get(item.containerName)
	if !ok {
		// TODO(xutao) clear ip?
		// TODO(xutao) clear lock?
		// TODO(xutao) move before create ip?
		return false
	}

	item.CleanRule(cont)
	for port, config := range item.ports {
		if cont != nil && cont.ip != "" {
			if config.containerIp == "" {
				config.containerIp = cont.ip
				item.ports[port] = config
			} else if config.containerIp != cont.ip {
				// remove old ip rules
				item.DeleteIpRule(&config)
				config.containerIp = cont.ip
				item.ports[port] = config
			}
		}

		if config.containerIp == "" {
			continue
		}

		switch state {
		case LockerStateCreated:
			item.CreateIpRule(&config)
			// calico
			item.CreateCalicoRule(&config, self.libnetwork)
		case LockerStateLocked:
			vi, _ := self.vDb.GetVirtualIp(item)
			item.CreateIpRule(&config)
			if vi.state == VirtualIpStateCreated || vi.state == VirtualIpStateUnused {
				// calico
				item.CreateCalicoRule(&config, self.libnetwork)
				self.CreateAppKey(item)
			}
		case LockerStateUnlocked:
			item.DeleteIpRule(&config)
		}
	}
	return true
}

func (self *Server) UnApplyVirtualIp(item *VirtualIpItem) bool {
	log.WithFields(logrus.Fields{
		"ip":    item.ip,
		"state": item.State(),
	}).Debug("Unapply virtual ip")

	for _, config := range item.ports {
		if config.containerIp != "" {
			item.DeleteIpRule(&config)
		}
	}

	self.DeleteAppKey(item)

	if item.port == "" {
		item.DeleteIp(self.iface)

		// check locked ip hostname
		state, err := self.UnLockVirtualIp(item.ip)
		if err != nil {
			log.WithFields(logrus.Fields{
				"ip":  item.ip,
				"err": err,
			}).Error("Cannot unlock virtual ip")
			return false
		}

		switch state {
		case LockerStateDeleted:
			// clear iptables
			cleanIptables(item.ip)
		case LockerStateLocked:
		case LockerStateUnlocked:
		case LockerStateError:
		}
	}

	// FIXME(xutao) clear calico
	/*
		for _, config := range item.ports {
			switch state {
			case LockerStateDeleted:
				if item.state == VirtualIpStateDeleted {
					// clear calico
				}
			case LockerStateLocked:
			case LockerStateUnlocked:
			case LockerStateError:
			}
		}
	*/
	return true
}

func (self *Server) IsFreeVirtualIp(item *VirtualIpItem) bool {
	if item.port == "" {
		if doIpAddrCheck(item.ip, self.iface) {
			return false
		}
	}
	if isInIptables(item.ip) {
		return false
	}
	return true
}

func (self *Server) IsAliveVirtualIp(ip string) bool {
	return doPing(ip)
}

func (self *VirtualIpDb) Add(item *VirtualIpItem) {
	// 1. remove old one if exist
	// 2. add new one
	db := self.db
	db.RWLock.Lock()
	defer db.RWLock.Unlock()
	key := item.GetKey()
	value := item
	if oldItem, ok := db.Get(key); ok {
		oldVirtualItem := oldItem.(*VirtualIpItem)
		item.next = oldVirtualItem
		item.parent = oldVirtualItem.parent
		oldVirtualItem.modifiedTime = item.modifiedTime
		db.Remove(key)
	}
	db.Add(key, value)
	log.WithFields(logrus.Fields{
		"ip": key,
	}).Debug("Add virtual ip item")
}

func (self *VirtualIpDb) Get(key string) (item *VirtualIpItem, ok bool) {
	db := self.db
	db.RWLock.RLock()
	defer db.RWLock.RUnlock()
	if item, ok := db.Get(key); ok {
		return item.(*VirtualIpItem), ok
	}
	return nil, false
}

func (self *VirtualIpDb) GetVirtualIp(item *VirtualIpItem) (vi *VirtualIp, ok bool) {
	if item.parent != nil {
		return item.parent, true
	}

	self.Lock()
	defer self.Unlock()
	key := item.GetKey()
	if vi, ok := self.ips[key]; ok {
		item.parent = vi
		return vi, true
	}

	vi = &VirtualIp{
		key:   item.GetKey(),
		state: VirtualIpStateCreated,
	}
	self.ips[key] = vi
	item.rwlock.Lock()
	defer item.rwlock.Unlock()
	item.parent = vi
	return vi, true
}

func (self *VirtualIpDb) Remove(key string) {
	db := self.db
	db.RWLock.Lock()
	defer db.RWLock.Unlock()
	db.Remove(key)

	self.Lock()
	defer self.Unlock()
	if _, ok := self.ips[key]; ok {
		delete(self.ips, key)
	}
}

func (self *VirtualIpDb) GetAll() []*VirtualIpItem {
	db := self.db
	db.RWLock.Lock()
	defer db.RWLock.Unlock()
	items := db.Items()
	virtualIpItems := make([]*VirtualIpItem, 0, len(items))
	for _, value := range items {
		virtualIpItems = append(virtualIpItems, value.(*VirtualIpItem))
	}
	return virtualIpItems
}

func (self *VirtualIpDb) SetUpdatedUnixTime(t int64) {
	db := self.db
	db.RWLock.Lock()
	defer db.RWLock.Unlock()
	self.latestUpdatedUnixTime = t
}

func (self *VirtualIpDb) GetUpdatedUnixTime() int64 {
	db := self.db
	db.RWLock.RLock()
	defer db.RWLock.RUnlock()
	return self.latestUpdatedUnixTime
}

func (self *VirtualIp) SetState(state int) bool {
	last := self.state
	self.state = state
	log.WithFields(logrus.Fields{
		"lastState":    VirtualIpState[last],
		"currentState": VirtualIpState[state],
		"item":         self.key,
	}).Info("Item state changed")
	return true
}

func (self *VirtualIpItem) GetKey() string {
	var key string
	if self.port != "" {
		key = fmt.Sprintf("%s:%s", self.ip, self.port)
	} else {
		key = self.ip
	}
	return key
}

func (self *VirtualIpItem) IsValidConfig() bool {
	return true
}

func (self *VirtualIpItem) State() string {
	return VirtualIpState[self.state]
}

func (self *VirtualIpItem) BroadcastIp(dev string) {
	if self.port != "" {
		return
	}
	// async
	go doArp(self.ip, dev)
}

func (self *VirtualIpItem) CreateIp(dev string) bool {
	if self.port != "" {
		return true
	}
	if doIpAddrCheck(self.ip, dev) {
		// ip addr is exist
		return true
	}
	if !doIpAddrAdd(self.ip, dev) {
		log.WithFields(logrus.Fields{
			"ip":  self.ip,
			"dev": dev,
		}).Error("cannot add ip addr")
		return false
	}
	return true
}

func (self *VirtualIpItem) DeleteIp(dev string) bool {
	if self.port != "" {
		return true
	}
	if !doIpAddrCheck(self.ip, dev) {
		// ip addr is not exist
		return true
	}
	if !doIpAddrDelete(self.ip, dev) {
		log.WithFields(logrus.Fields{
			"ip":  self.ip,
			"dev": dev,
		}).Error("cannot delete ip addr")
		return false
	}
	return true
}

func (self *VirtualIpItem) CreateIpRule(config *VirtualIpPortItem) bool {
	comment := fmt.Sprintf("%s.%s", self.appName, self.procName)
	preroutingAcl := &ItemAcl{
		srcIp:   self.ip,
		srcPort: config.port,
		dstIp:   config.containerIp,
		dstPort: config.containerPort,
		proto:   config.proto,
		chain:   "lain-PREROUTING",
		action:  "-A",
		comment: comment,
	}
	ok := doIptablesAddAcl(preroutingAcl)
	if !ok {
		return false
	}
	outputAcl := &ItemAcl{
		srcIp:   self.ip,
		srcPort: config.port,
		dstIp:   config.containerIp,
		dstPort: config.containerPort,
		proto:   config.proto,
		chain:   "lain-OUTPUT",
		device:  "lo",
		action:  "-A",
		comment: comment,
	}
	ok = doIptablesAddAcl(outputAcl)
	if !ok {
		return false
	}
	return true
}

func (self *VirtualIpItem) DeleteIpRule(config *VirtualIpPortItem) bool {
	comment := fmt.Sprintf("%s.%s", self.appName, self.procName)
	preroutingAcl := &ItemAcl{
		srcIp:   self.ip,
		srcPort: config.port,
		dstIp:   config.containerIp,
		dstPort: config.containerPort,
		proto:   config.proto,
		chain:   "lain-PREROUTING",
		action:  "-D",
		comment: comment,
	}
	ok := doIptablesDeleteAcl(preroutingAcl)
	if !ok {
		return false
	}
	outputAcl := &ItemAcl{
		srcIp:   self.ip,
		srcPort: config.port,
		dstIp:   config.containerIp,
		dstPort: config.containerPort,
		proto:   config.proto,
		chain:   "lain-OUTPUT",
		device:  "lo",
		action:  "-D",
		comment: comment,
	}
	ok = doIptablesDeleteAcl(outputAcl)
	if !ok {
		return false
	}
	return true
}

func (self *VirtualIpItem) CreateCalicoRule(config *VirtualIpPortItem, libnetwork bool) bool {
	profileName := self.appName
	if !libnetwork {
		// backwards-compatible for lain 1.0.X
		profileName = fmt.Sprintf("%x", md5.Sum([]byte(self.appName)))
		if !doCalicoAddProfileDefaultRule(profileName) {
			doCalicoAddProfile(profileName)
			doCalicoAddProfileDefaultRule(profileName)
		}
	}

	if !doCalicoAddProfileRule(profileName, config.proto, config.containerPort) {
		return false
	}
	return true
}

func (self *VirtualIpItem) CleanRule(cont *ContainerItem) {
	if cont == nil || cont.ip == "" {
		log.WithFields(logrus.Fields{
			"item": self.GetKey(),
		}).Error("No valid container for item")
		return
	}
	item := self.next
	for {
		if item == nil {
			break
		}
		for port, config := range item.ports {
			if config.containerIp == "" {
				continue
			}
			// outdated container ip
			if config.containerIp != cont.ip {
				item.DeleteIpRule(&config)
				continue
			}
			// deleted port
			if _, ok := self.ports[port]; !ok {
				item.DeleteIpRule(&config)
				continue
			}
		}
		item = item.next
	}
	self.next = nil
}

//TODO(xutao) DeleteCalicoRule
