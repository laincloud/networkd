package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/Sirupsen/logrus"
	etcd "github.com/coreos/etcd/client"
	"golang.org/x/net/context"

	"github.com/docker/leadership"
	"github.com/docker/libkv"
	"github.com/docker/libkv/store"
	kvetcd "github.com/docker/libkv/store/etcd"
	"github.com/fsouza/go-dockerclient"
	lainlet "github.com/laincloud/lainlet/client"
	"github.com/laincloud/networkd/acl"
	"github.com/laincloud/networkd/dnsmasq"
	"github.com/projectcalico/libcalico-go/lib/api"
	calicoetcd "github.com/projectcalico/libcalico-go/lib/backend/etcd"
	calico "github.com/projectcalico/libcalico-go/lib/client"
)

const (
	LockerStateNone     = iota // no lock
	LockerStateCreated  = iota // create locker
	LockerStateLocked   = iota // locked && lock by me
	LockerStateUnlocked = iota // locked && not lock by me
	LockerStateDeleted  = iota // delete locker
	LockerStateError    = iota
)

const (
	DOMAINLAIN = "lain"
)

type LockedIp struct {
	ip            string
	modifiedIndex uint64
}

type Server struct {
	ip            string
	iface         string
	hostname      string
	domain        string
	lockedIps     []LockedIp
	libnetwork    bool
	wg            sync.WaitGroup
	docker        *docker.Client
	etcd          *etcd.Client
	calico        *calico.Client
	libkv         store.Store
	lainlet       *lainlet.Client
	vDb           *VirtualIpDb
	cDb           *ContainerDb
	stopCh        chan struct{}
	lainletStopCh chan struct{}
	eventCh       chan int
	// resolv.conf
	resolvConfFlag      bool
	resolvConfStopCh    chan struct{}
	resolvConfIsRunning bool
	// elect
	electStopCh    chan struct{}
	electIsRunning bool
	// health
	healthStopCh    chan struct{}
	healthIsRunning bool
	// dnsmasq
	dnsmasq       *dnsmasq.Server
	dnsmasqFlag   bool
	dnsmasqHost   string
	dnsmasqServer string
	dnsmasqDomain string
	//Acl
	acl     *acl.Acl
	aclFlag bool
	// swarm
	swarmFlag      bool
	swarmStopCh    chan struct{}
	swarmIsRunning bool
	// tinydns
	tinydnsFlag      bool
	tinydnsStopCh    chan struct{}
	tinydnsIsRunning bool
	// webrouter
	webrouterFlag      bool
	webrouterStopCh    chan struct{}
	webrouterIsRunning bool
	// deployd
	deploydFlag      bool
	deploydStopCh    chan struct{}
	deploydIsRunning bool
	// streamrouter
	streamrouterFlag      bool
	streamrouterStopCh    chan struct{}
	streamrouterIsRunning bool
}

type JSONVirtualIpPortConfig struct {
	Src   string `json:"src"`
	Proto string `json:"proto"`
	Dest  string `json:"dest"`
}
type JSONVirtualIpConfig struct {
	App           string                    `json:"app"`
	Proc          string                    `json:"proc"`
	Ports         []JSONVirtualIpPortConfig `json:"ports"`
	ExcludedNodes []string                  `json:"excluded_nodes"`
}

type JSONLainletContainer struct {
	AppName    string `json:"app"`
	ProcName   string `json:"proc"`
	NodeName   string `json:"nodename"`
	NodeIP     string `json:"nodeip"`
	IP         string `json:"ip"`
	Port       int    `json:"port"`
	InstanceNo int    `json:"instanceNo"`
}
type JSONLainletContainers map[string]JSONLainletContainer

const APIVersion = "1.18"
const EtcdLainVirtualIpKey = "/lain/config/vips"
const EtcdNetworkdVirtualIpKey = "/lain/networkd/vips"
const LainLetVirtualIpKey = "vips"
const EtcdNetworkdLeaderKey = "/lain/networkd/leader"
const EtcdAppNetworkdKey = "/lain/networkd/apps"
const EtcdSwarmKey = "/lain/swarm/docker/swarm/leader"
const EtcdDeploydKey = "/lain/deployd/leader"

// eg: /lain/networkd/containers/webrouter/worker/1 has the vip list for webrouter instance 1
const EtcdNetworkdContainerVips = "/lain/networkd/containers"

func init() {
	kvetcd.Register()
}

func (self *Server) InitFlag(dnsmasq bool, tinydns bool, swarm bool, webrouter bool, deployd bool, acl bool, resolvConf bool, streamrouter bool) {
	self.dnsmasqFlag = dnsmasq
	self.tinydnsFlag = tinydns
	self.swarmFlag = swarm
	self.webrouterFlag = webrouter
	self.streamrouterFlag = streamrouter
	self.deploydFlag = deployd
	self.aclFlag = acl
	self.resolvConfFlag = resolvConf
}

func (self *Server) InitDocker(endpoint string) {
	// calico powerstrip need explicit version
	client, err := docker.NewVersionedClient(endpoint, APIVersion)
	if err != nil {
		log.Fatal(err)
	}
	self.docker = client
	// TODO(xutao) check docker daemon status
}

func (self *Server) InitLibNetwork(flag bool) {
	self.libnetwork = flag
}

func (self *Server) InitEtcd(endpoint string) {
	cfg := etcd.Config{
		Endpoints: []string{endpoint},
		Transport: etcd.DefaultTransport,
		// set timeout per request to fail fast when the target endpoint is unavailable
		// TODO(xutao) disable HeaderTimeoutPerRequest for etcd proxy
		// For watch request, server returns the header immediately to notify Client
		// watch start. But if server is behind some kind of proxy, the response
		// header may be cached at proxy, and Client cannot rely on this behavior.
		//HeaderTimeoutPerRequest: time.Second,
	}
	c, err := etcd.New(cfg)
	if err != nil {
		log.Fatal(err)
	}
	self.etcd = &c
	// TODO(xutao) check etcd status
}

func (self *Server) InitCalico(endpoint string) {
	config := api.CalicoAPIConfig{
		Spec: api.CalicoAPIConfigSpec{
			DatastoreType: api.EtcdV2,
			EtcdConfig: calicoetcd.EtcdConfig{
				EtcdEndpoints: endpoint,
			},
		},
	}
	c, err := calico.New(config)
	if err != nil {
		log.Fatal(err)
	}
	self.calico = c
}

func (self *Server) InitLibkv(endpoint string) {
	etcdEndpoint := endpoint
	if strings.HasPrefix(endpoint, "http://") {
		etcdEndpoint = endpoint[7:]
	}
	kv, err := libkv.NewStore(
		store.ETCD,
		[]string{etcdEndpoint},
		&store.Config{
			ConnectionTimeout: 10 * time.Second,
		},
	)
	if err != nil {
		log.Fatal(err)
	}
	self.libkv = kv
}

func (self *Server) InitLainlet(endpoint string) error {
	client := lainlet.New(endpoint)
	retryCounter := 0
	for {
		_, err := client.Get("/debug", 0)
		if err == nil {
			// TODO(xutao) check lainlet status
			// TODO(xutao) check lainlet version
			break
		}
		log.WithFields(logrus.Fields{
			"err":          err,
			"retryCounter": retryCounter,
		}).Error("Fail to connect lainlet")
		time.Sleep(30 * time.Second)
		retryCounter++
		continue
	}
	self.lainlet = client
	return nil
}

func (self *Server) InitInterface(iface string) {
	self.iface = iface
}

func (self *Server) InitIptables() {
	initIptables()
}

func (self *Server) InitDomain(domain string) {
	if domain != "" {
		self.domain = domain
		log.Info(fmt.Sprintf("Domain %s", domain))
	}
}

func (self *Server) InitHostname(hostname string) {
	if hostname != "" {
		self.hostname = hostname
		return
	}
	var err error
	if hostname, err = os.Hostname(); err != nil {
		log.Fatal(err)
	}
	self.hostname = hostname
	log.Info(fmt.Sprintf("Hostname %s", self.hostname))
}

func (self *Server) InitAddress(ip string) {
	if ip == "" {
		// default ip: iface's first ip
		ifi, err := net.InterfaceByName(self.iface)
		if err != nil {
			log.Fatal(err)
		}
		addrs, err := ifi.Addrs()
		if err != nil {
			log.Fatal(err)
		}
		for _, address := range addrs {
			// check the address type and if it is not a loopback the display it
			if ipNet, ok := address.(*net.IPNet); ok && !ipNet.IP.IsLoopback() {
				if ipNet.IP.To4() != nil {
					ip = ipNet.IP.String()
					break
				}
			}
		}
	}
	if ip == "" {
		log.Fatal("No net.address")
	}
	self.ip = ip
	log.Info(fmt.Sprintf("HostIP %s", self.ip))
}

func (self *Server) InitDnsmasq(host string, server string, domain string, extra bool) {
	// TODO(xutao) check host & server
	self.dnsmasqHost = host
	self.dnsmasqServer = server
	self.dnsmasqDomain = domain
	self.dnsmasq = dnsmasq.New(self.ip, self.libkv, self.lainlet, log, host, server, domain, extra)
	self.tinydnsStopCh = make(chan struct{})
	self.tinydnsIsRunning = false
	self.swarmStopCh = make(chan struct{})
	self.swarmIsRunning = false
}

func (self *Server) InitAcl() {
	self.acl = acl.New(log, self.lainlet)
}

func (self *Server) InitWebrouter() {
	self.webrouterStopCh = make(chan struct{})
	self.webrouterIsRunning = false
}

func (self *Server) InitStreamrouter() {
	self.streamrouterStopCh = make(chan struct{})
	self.streamrouterIsRunning = false
}

func (self *Server) InitDeployd() {
	self.deploydStopCh = make(chan struct{})
	self.deploydIsRunning = false
}

func (self *Server) InitEventChan() {
	self.eventCh = make(chan int)
	self.stopCh = make(chan struct{})
	self.lainletStopCh = make(chan struct{})
	self.electStopCh = make(chan struct{})
	self.healthStopCh = make(chan struct{})
}

func (self *Server) ListLainletContainers() (containers JSONLainletContainers, err error) {
	url := fmt.Sprintf("/v2/containers?nodename=%s", self.hostname)
	data, err := self.lainlet.Get(url, 0)
	if err != nil {
		return containers, err
	}
	err = json.Unmarshal(data, &containers)
	return containers, err
}

func (self *Server) FetchNetworkdVirtualIps() {
	kapi := etcd.NewKeysAPI(*self.etcd)
	resp, err := kapi.Get(context.Background(), EtcdNetworkdVirtualIpKey, &etcd.GetOptions{Recursive: true})

	if err != nil {
		log.WithFields(logrus.Fields{
			"key": EtcdNetworkdVirtualIpKey,
			"err": err,
		}).Debug("Fail to get etcd key")
		return
	}

	if resp.Node.Dir != true {
		log.WithFields(logrus.Fields{
			"key": EtcdNetworkdVirtualIpKey,
		}).Error("Etcd key is not dir")
	}

	var ips []LockedIp
	prefixKeyLength := len(EtcdNetworkdVirtualIpKey) + 1
	for _, node := range resp.Node.Nodes {
		vip := node.Key[prefixKeyLength:]
		if !strings.HasSuffix(vip, ".lock") {
			continue
		}
		vip = vip[:len(vip)-5]
		paresedIp := net.ParseIP(vip)
		if paresedIp.To4() == nil {
			continue
		}
		ips = append(ips, LockedIp{ip: vip, modifiedIndex: node.ModifiedIndex})
	}
	self.lockedIps = ips
}

func (self *Server) WatchNetworkdVirtualIps() {
	ctx := context.Background()
	kapi := etcd.NewKeysAPI(*self.etcd)
	watcher := kapi.Watcher(EtcdNetworkdVirtualIpKey, &etcd.WatcherOptions{Recursive: true})
	for {
		resp, err := watcher.Next(ctx)
		if err != nil {
			log.WithFields(logrus.Fields{
				"key": EtcdNetworkdVirtualIpKey,
				"err": err,
			}).Error("Fail to watch etcd key")
			time.Sleep(30 * time.Second)
			watcher = kapi.Watcher(EtcdNetworkdVirtualIpKey, &etcd.WatcherOptions{Recursive: true})
			continue
		}

		log.WithFields(logrus.Fields{
			"key":    resp.Node.Key,
			"value":  resp.Node.Value,
			"action": resp.Action,
		}).Debug("Virtual ip lock changed")

		// TODO(xutao) get, set, delete, update, create, compareAndSwap, compareAndDelete and expire
		// TODO(xutao) check owned ip
		if resp.Action != "delete" || resp.Action != "compareAndDelete" {
			continue
		}
		log.Debug("Send virtual ip lock event")
		self.eventCh <- 1
	}

}

func (self *Server) WatchLainlet(watchKey string, stopCh <-chan struct{}, callback func(event *lainlet.Response)) {
	retryCounter := 0
	for {
		ctx := context.Background()
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
		breakWatch := false
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
						log.WithFields(logrus.Fields{
							"id":    event.Id,
							"event": event.Event,
						}).Error("Fail to watch lainlet")
						time.Sleep(5 * time.Second)
					}
					continue
				}
				callback(event)
			case <-stopCh:
				return
			}
			if breakWatch {
				break
			}
		}
		log.Error("Fail to watch lainlet")
	}

}

func (self *Server) WatchLainletVirtualIps() {
	self.WatchLainlet("/v2/configwatcher?target=vips&heartbeat=5", nil, func(event *lainlet.Response) {
		keyPrefixLength := len(LainLetVirtualIpKey) + 1
		currentUnixTime := time.Now().Unix()
		var vips interface{}
		err := json.Unmarshal(event.Data, &vips)
		for key, value := range vips.(map[string]interface{}) {
			virtualIpKey := key[keyPrefixLength:]
			virtualPort := ""
			colonCount := strings.Count(virtualIpKey, ":")
			if colonCount == 1 {
				splitKey := strings.SplitN(virtualIpKey, ":", 2)
				virtualIpKey, virtualPort = splitKey[0], splitKey[1]
			} else if colonCount > 1 {
				log.WithFields(logrus.Fields{
					"virtualIpKey": virtualIpKey,
					"value":        value.(string),
				}).Error("Invalid key")
				return
			}
			paresedIp := net.ParseIP(virtualIpKey)
			if paresedIp.To4() == nil {
				log.WithFields(logrus.Fields{
					"virtualIpKey": virtualIpKey,
					"value":        value.(string),
				}).Error("Invalid key")
				return
			}
			log.WithFields(logrus.Fields{
				"virtualIpKey": virtualIpKey,
				"value":        value.(string),
			}).Debug("Get virutal ip config from lainlet")
			var ipConfig JSONVirtualIpConfig
			err = json.Unmarshal([]byte(value.(string)), &ipConfig)
			if err != nil {
				log.WithFields(logrus.Fields{
					"key":    fmt.Sprintf("/lain/config/%s", key),
					"reason": err,
				}).Warn("Cannot parse virtual ip config")
				return
			}
			log.WithFields(logrus.Fields{
				"virtualIpKey": virtualIpKey,
				"ipConfig":     ipConfig,
			}).Debug("Get virutal ip json config from lainlet")
			if virtualPort != "" {
				// TODO(xutao) check ports in config
			}
			if virtualIpKey == "0.0.0.0" {
				// replace 0.0.0.0 as host ip
				virtualIpKey = self.ip
			}
			self.AddVirtualIp(virtualIpKey, virtualPort, ipConfig, currentUnixTime)
		}
		self.vDb.SetUpdatedUnixTime(currentUnixTime)
		log.Debug("Send virtual ip event")
		self.eventCh <- 1
	})
}

func (self *Server) WatchProcIps(stopWatchCh <-chan struct{}, app string, proc string) <-chan int {
	// TODO(xutao) watch lainlet
	kv := self.libkv
	key := fmt.Sprintf("%s/%s/%s", EtcdAppNetworkdKey, app, proc)
	eventCh := make(chan int)
	go func() {
		defer close(eventCh)
		for {
			retryCounter := 0
			breakWatch := false
			for {
				exists, err := kv.Exists(key)
				if err != nil {
					// TODO(xutao) error processing
					log.WithFields(logrus.Fields{
						"key":          key,
						"retryCounter": retryCounter,
					}).Error("Cannot get etcd key")
					time.Sleep(time.Second * 15)
					retryCounter++
					continue
				}
				if !exists {
					err = kv.Put(key, []byte(key), &store.WriteOptions{IsDir: true})
					if err != nil {
						// TODO(xutao) error processing
						log.WithFields(logrus.Fields{
							"key":          key,
							"retryCounter": retryCounter,
						}).Error("Cannot initialize etcd key")
						time.Sleep(time.Second * 15)
						retryCounter++
						continue
					}
				}
				// everything is ok
				break
			}

			stopCh := make(chan struct{})
			events, err := kv.WatchTree(key, stopCh)
			if err != nil {
				log.WithFields(logrus.Fields{
					"key": key,
				}).Error("Cannot watch etcd key")
				time.Sleep(time.Second * 15)
				continue
			}
			for {
				select {
				case pairs, ok := <-events:
					if !ok {
						breakWatch = true
						break
					}

					for _, pair := range pairs {
						log.WithFields(logrus.Fields{
							"key":   pair.Key,
							"value": string(pair.Value),
						}).Debug("Value changed on key")
					}
					eventCh <- 1
				case <-stopWatchCh:
					stopCh <- struct{}{}
					close(stopCh)
					return
				}
				if breakWatch {
					break
				}
			}
		}
	}()
	return eventCh
}

// TODO(xutao) move to webrouter app
func (self *Server) WatchWebrouterIps(stopWatchCh <-chan struct{}) <-chan int {
	return self.WatchProcIps(stopWatchCh, "webrouter", "worker")
}

func (self *Server) ApplyWebrouterIps() {
	kv := self.libkv
	var servers []string
	key := fmt.Sprintf("%s/webrouter/worker", EtcdAppNetworkdKey)
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
		ip, _ := splitKey[0], splitKey[1]
		servers = append(servers, ip)
	}

	if len(servers) < 1 {
		return
	}

	// update webrouter dns A Record
	uniqServers := make(map[string]interface{})
	for _, server := range servers {
		uniqServers[server] = struct{}{}
	}
	data := make([]string, 0)
	for server, _ := range uniqServers {
		data = append(data, fmt.Sprintf("+webrouter.lain:%s:300", server))
	}
	self.AddTinydnsDomain("webrouter.lain", data)

	// update main domain as DNS NS
	domains := make([]string, 0)
	if self.domain != "" {
		domains = append(domains, self.domain)
	}
	if self.domain != DOMAINLAIN {
		domains = append(domains, DOMAINLAIN)
	}
	for _, domain := range domains {
		data := make([]string, 0)
		for server, _ := range uniqServers {
			data = append(data, fmt.Sprintf("+*.%s:%s:300", domain, server))
			data = append(data, fmt.Sprintf(".%s:%s:a:300", domain, server))
		}
		self.AddTinydnsDomain(domain, data)
	}
}

func (self *Server) WatchLeaderIps(stopWatchCh <-chan struct{}, key string) <-chan int {
	kv := self.libkv
	eventCh := make(chan int)
	go func() {
		for {
			var lastValue []byte
			breakWatch := false
			retryCounter := 0
			for {
				exists, err := kv.Exists(key)
				if err != nil {
					log.WithFields(logrus.Fields{
						"key":          key,
						"retryCounter": retryCounter,
						"err":          err,
					}).Error("Cannot get swarm key")
					retryCounter++
					time.Sleep(30 * time.Second)
					continue
				}
				if !exists {
					log.WithFields(logrus.Fields{
						"key":          key,
						"retryCounter": retryCounter,
					}).Debug("No leader key")
					retryCounter++
					time.Sleep(15 * time.Second)
					continue
				}
				// everything is ok
				break
			}

			stopCh := make(chan struct{})
			events, err := kv.Watch(key, stopCh)
			if err != nil {
				log.WithFields(logrus.Fields{
					"key": key,
				}).Error("Cannot watch etcd key")
				time.Sleep(time.Second * 15)
				continue
			}
			for {
				select {
				case pair, ok := <-events:
					if !ok {
						breakWatch = true
						break
					}

					log.WithFields(logrus.Fields{
						"key":   pair.Key,
						"value": string(pair.Value),
						"index": pair.LastIndex,
					}).Debug("Get leader key")

					// swarm filter for updating key every 5s
					if !bytes.Equal(lastValue, pair.Value) {
						eventCh <- 1
						lastValue = pair.Value
					}
				case <-stopWatchCh:
					stopCh <- struct{}{}
					close(stopCh)
					return
				}
				if breakWatch {
					break
				}
			}
		}
	}()
	return eventCh
}

func (self *Server) WatchDeploydIps(stopWatchCh <-chan struct{}) <-chan int {
	return self.WatchLeaderIps(stopWatchCh, EtcdDeploydKey)
}

func (self *Server) ApplyDeploydIps() {
	kv := self.libkv
	key := EtcdDeploydKey
	pair, err := kv.Get(key)
	if err != nil {
		log.WithFields(logrus.Fields{
			"key": key,
			"err": err,
		}).Error("Cannot get deployd key")
		return
	}
	log.WithFields(logrus.Fields{
		"key":   pair.Key,
		"value": string(pair.Value),
	}).Debug("Get deployd key")

	value := string(pair.Value)
	splitKey := strings.SplitN(value, ":", 2)
	ip, _ := splitKey[0], splitKey[1]
	ips := []string{ip}
	self.dnsmasq.AddAddress("deployd.lain", ips, "")
}

func (self *Server) WatchSwarmIps(stopWatchCh <-chan struct{}) <-chan int {
	// swarm leader key default ttl: 20
	// TODO(xutao) watch lainlet
	return self.WatchLeaderIps(stopWatchCh, EtcdSwarmKey)
}

func (self *Server) ApplySwarmIps() {
	kv := self.libkv
	key := EtcdSwarmKey
	pair, err := kv.Get(key)
	if err != nil {
		log.WithFields(logrus.Fields{
			"key": key,
			"err": err,
		}).Error("Cannot get swarm key")
		return
	}
	log.WithFields(logrus.Fields{
		"key":   pair.Key,
		"value": string(pair.Value),
	}).Debug("Get swarm key")

	value := string(pair.Value)
	splitKey := strings.SplitN(value, ":", 2)
	ip, _ := splitKey[0], splitKey[1]
	ips := []string{ip}
	self.dnsmasq.AddAddress("swarm.lain", ips, "")
}

func (self *Server) RunHealth() {
	if self.healthIsRunning {
		return
	}
	log.Info("Run health")
	self.healthIsRunning = true
	go func() {
		self.wg.Add(1)
		defer func() {
			log.Info("health done")
			self.wg.Done()
		}()
		tickCh := time.NewTicker(time.Second * 30).C
		for {
			select {
			case <-tickCh:
				self.FetchNetworkdVirtualIps()
				for _, lockedIp := range self.lockedIps {
					ip := lockedIp.ip
					isAlive := self.IsAliveVirtualIp(ip)
					if isAlive {
						continue
					}
					// remove lock
					log.WithFields(logrus.Fields{
						"ip": ip,
					}).Info("Remove dead ip lock")
					self.RemoveVirtualIpLock(lockedIp)
				}
			case <-self.healthStopCh:
				return
			}
		}
	}()
}

func (self *Server) StopHealth() {
	log.Debug("Stop health")
	if self.healthIsRunning {
		self.healthIsRunning = false
		self.healthStopCh <- struct{}{}
	}
}

func (self *Server) CreateAppKey(item *VirtualIpItem) {
	if item.port == "" {
		return
	}
	value := self.hostname

	if self.GetAppKey(item) == value {
		return
	}

	key := fmt.Sprintf("%s/%s/%s/%s:%s", EtcdAppNetworkdKey, item.appName, item.procName, item.ip, item.port)
	kapi := etcd.NewKeysAPI(*self.etcd)
	// TODO(xutao) retry
	_, _ = kapi.Set(
		context.Background(),
		key,
		value,
		&etcd.SetOptions{},
	)
}

func (self *Server) GetAppKey(item *VirtualIpItem) string {
	key := fmt.Sprintf("%s/%s/%s/%s:%s", EtcdAppNetworkdKey, item.appName, item.procName, item.ip, item.port)
	kapi := etcd.NewKeysAPI(*self.etcd)

	retryCounter := 0
	for {
		resp, err := kapi.Get(context.Background(), key, nil)
		if err != nil {
			if etcdErr, ok := err.(etcd.Error); ok {
				switch etcdErr.Code {
				case etcd.ErrorCodeKeyNotFound:
					return ""
				default:
					// FIXME(xutao) "client: etcd cluster is unavailable or misconfigured"
					log.WithFields(logrus.Fields{
						"err":         err,
						"etcdErrCode": etcdErr.Code,
						"key":         key,
					}).Error("Fail to get key")
				}
			}

			if retryCounter >= 3 {
				// FIXME(xutao) "client: etcd cluster is unavailable or misconfigured"
				log.WithFields(logrus.Fields{
					"err":          err,
					"key":          key,
					"retryCounter": retryCounter,
				}).Fatal("Fail to get key")
			}
			time.Sleep(time.Second)
			retryCounter++
			continue
		}

		if resp.Node.Dir != false {
			log.WithFields(logrus.Fields{
				"key": key,
			}).Fatal("Etcd key is dir")
		}

		return resp.Node.Value
	}
}

func (self *Server) DeleteAppKey(item *VirtualIpItem) {
	if item.port == "" {
		return
	}

	if self.GetAppKey(item) == "" {
		return
	}

	key := fmt.Sprintf("%s/%s/%s/%s:%s", EtcdAppNetworkdKey, item.appName, item.procName, item.ip, item.port)
	kapi := etcd.NewKeysAPI(*self.etcd)
	// TODO(xutao) retry
	_, _ = kapi.Delete(
		context.Background(),
		key,
		&etcd.DeleteOptions{},
	)
}

func (self *Server) RunElect() {
	self.electIsRunning = true
	candidate := leadership.NewCandidate(self.libkv,
		EtcdNetworkdLeaderKey,
		self.hostname,
		15*time.Second)
	stopCh := make(chan struct{})
	go self.WatchElect(stopCh)
	go func() {
		defer close(stopCh)
		for {
			breakWatch := false
			electedCh, errCh := candidate.RunForElection()
			for {
				select {
				case isElected := <-electedCh:
					if isElected {
						log.WithFields(logrus.Fields{
							"host": self.hostname,
						}).Info("Change to leader")
					} else {
						log.WithFields(logrus.Fields{
							"host": self.hostname,
						}).Info("Change to follower")
					}
				case err := <-errCh:
					log.Error(err)
					breakWatch = true
					break
				case <-self.electStopCh:
					candidate.Resign()
					stopCh <- struct{}{}
					return
				}
				if breakWatch {
					break
				}
			}
			time.Sleep(30 * time.Second)
		}
	}()
}

func (self *Server) WatchElect(stopWatchCh <-chan struct{}) {
	// TODO(xutao) watch lainlet
	kv := self.libkv
	key := EtcdNetworkdLeaderKey
	for {
		breakWatch := false
		retryCounter := 0
		for {
			exists, err := kv.Exists(key)
			if err != nil {
				log.WithFields(logrus.Fields{
					"key":          key,
					"retryCounter": retryCounter,
					"err":          err,
				}).Error("Cannot get networkd leader key")
				retryCounter++
				time.Sleep(30 * time.Second)
				continue
			}
			if !exists {
				log.WithFields(logrus.Fields{
					"key":          key,
					"retryCounter": retryCounter,
				}).Debug("No networkd leader key")
				retryCounter++
				time.Sleep(15 * time.Second)
				continue
			}
			// everything is ok
			break
		}

		stopCh := make(chan struct{})
		events, err := kv.Watch(key, stopCh)
		if err != nil {
			log.WithFields(logrus.Fields{
				"key": key,
			}).Error("Cannot watch etcd key")
			time.Sleep(time.Second * 15)
			continue
		}
		for {
			select {
			case pair, ok := <-events:
				if !ok {
					breakWatch = true
					break
				}

				log.WithFields(logrus.Fields{
					"key":   pair.Key,
					"value": string(pair.Value),
					"index": pair.LastIndex,
				}).Debug("Get networkd leader key")

				if string(pair.Value) == self.hostname {
					self.RunHealth()
					if self.deploydFlag {
						self.RunDeployd()
					}
					if self.webrouterFlag {
						self.RunWebrouter()
					}
					if self.streamrouterFlag {
						self.RunStreamrouter()
					}
					if self.dnsmasqFlag {
						if self.swarmFlag {
							self.RunSwarm()
						}
						if self.tinydnsFlag {
							self.RunTinydns()
						}
					}
				} else {
					if self.dnsmasqFlag {
						if self.tinydnsFlag {
							self.StopTinydns()
						}
						if self.swarmFlag {
							self.StopSwarm()
						}
					}
					if self.webrouterFlag {
						self.StopWebrouter()
					}
					if self.streamrouterFlag {
						self.StopStreamrouter()
					}
					if self.deploydFlag {
						self.StopDeployd()
					}
					self.StopHealth()
				}
			case <-stopWatchCh:
				stopCh <- struct{}{}
				close(stopCh)
				return
			}
			if breakWatch {
				break
			}
		}
	}

}

func (self *Server) StopElect() {
	log.Debug("Stop elect")
	if self.electIsRunning {
		self.electIsRunning = false
		self.electStopCh <- struct{}{}
	}
}

func (self *Server) LockVirtualIp(ip string, containerName string) (state int, err error) {

	self.CreateContainerVipKey(containerName, ip)

	// TODO(xutao) remove log.Fatal?
	key := fmt.Sprintf("%s/%s.lock", EtcdNetworkdVirtualIpKey, ip)
	value := self.hostname
	kapi := etcd.NewKeysAPI(*self.etcd)

	added := 0
	tmpOwner := self.GetLockedVipHostname(ip)
	if tmpOwner != self.hostname {
		added = 1
	}
	if !self.isContainerVipBalanced(containerName, added) {
		self.DeleteContainerVip(containerName, ip)
		return LockerStateUnlocked, nil
	}

	retryCounter := 0
	for {
		resp, err := kapi.Set(
			context.Background(),
			key,
			value,
			&etcd.SetOptions{PrevExist: etcd.PrevNoExist, TTL: 30 * time.Second},
		)

		if err != nil {
			if etcdErr, ok := err.(etcd.Error); ok {
				switch etcdErr.Code {
				case etcd.ErrorCodeNodeExist:
					lockOwner := self.GetLockedVipHostname(ip)
					if lockOwner != "" && lockOwner == self.hostname {
						timeInterval := VipLockTTL
						if !self.isContainerVipBalanced(containerName, 0) {
							timeInterval = 1
							log.Info("find unbalanced vip of container")
						} else {
							log.Debug("vip is balanced")
						}
						// refresh ttl
						if _, e := kapi.Set(
							context.Background(),
							key,
							value,
							&etcd.SetOptions{PrevExist: etcd.PrevExist, TTL: time.Duration(timeInterval) * time.Second},
						); e != nil {
							log.WithFields(logrus.Fields{
								"err": e,
								"key": key,
							}).Error("fresh leader key ttl Failed")
						}
						self.AddContainerVip(containerName, ip)
						return LockerStateLocked, nil
					}
					self.DeleteContainerVip(containerName, ip)
					return LockerStateUnlocked, nil
				default:
					// FIXME(xutao) "client: etcd cluster is unavailable or misconfigured"
					log.WithFields(logrus.Fields{
						"err":          err,
						"etcdErrCode":  etcdErr.Code,
						"retryCounter": retryCounter,
						"key":          key,
					}).Fatal("Fail to set key")
				}
			}

			if retryCounter >= 3 {
				// FIXME(xutao) "client: etcd cluster is unavailable or misconfigured"
				log.WithFields(logrus.Fields{
					"err":          err,
					"key":          key,
					"retryCounter": retryCounter,
				}).Fatal("Fail to set key")
			}
			time.Sleep(time.Second)
			retryCounter++
			continue
		}

		PrevIndex := resp.Node.ModifiedIndex
		resp, err = kapi.Set(
			context.Background(),
			key,
			value,
			&etcd.SetOptions{PrevIndex: PrevIndex, TTL: VipLockTTL * time.Second},
		)
		if err != nil {
			if retryCounter >= 3 {
				// FIXME(xutao) "client: etcd cluster is unavailable or misconfigured"
				log.WithFields(logrus.Fields{
					"err":          err,
					"key":          key,
					"retryCounter": retryCounter,
				}).Fatal("Fail to set key")
			}
			time.Sleep(time.Second)
			retryCounter++
			continue
		}

		self.AddContainerVip(containerName, ip)
		log.WithFields(logrus.Fields{
			"key":   key,
			"value": value,
		}).Info("Success to Lock key")
		return LockerStateCreated, nil
	}
}

func (self *Server) GetLockedVipHostname(ip string) string {
	// TODO(xutao) remove log.Fatal?
	key := fmt.Sprintf("%s/%s.lock", EtcdNetworkdVirtualIpKey, ip)
	kapi := etcd.NewKeysAPI(*self.etcd)

	retryCounter := 0
	for {
		// TODO(xutao) use prevIndex
		resp, err := kapi.Get(context.Background(), key, nil)

		if err != nil {
			if etcdErr, ok := err.(etcd.Error); ok {
				switch etcdErr.Code {
				case etcd.ErrorCodeKeyNotFound:
					return ""
				default:
					// FIXME(xutao) "client: etcd cluster is unavailable or misconfigured"
					log.WithFields(logrus.Fields{
						"err":          err,
						"etcdErrCode":  etcdErr.Code,
						"retryCounter": retryCounter,
						"key":          key,
					}).Fatal("Fail to get key")
				}
			}

			if retryCounter >= 3 {
				// FIXME(xutao) "client: etcd cluster is unavailable or misconfigured"
				log.WithFields(logrus.Fields{
					"err":          err,
					"key":          key,
					"retryCounter": retryCounter,
				}).Fatal("Fail to get key")
			}
			time.Sleep(time.Second)
			retryCounter++
			continue
		}

		if resp.Node.Dir != false {
			log.WithFields(logrus.Fields{
				"key": key,
			}).Error("Etcd key is dir")
			return ""
		}

		return resp.Node.Value
	}
}

func (self *Server) UnLockVirtualIp(ip string, containerName string) (state int, err error) {
	// TODO(xutao) remove log.Fatal?
	key := fmt.Sprintf("%s/%s.lock", EtcdNetworkdVirtualIpKey, ip)
	value := self.hostname
	kapi := etcd.NewKeysAPI(*self.etcd)

	retryCounter := 0
	for {
		// TODO(xutao) use prevIndex
		resp, err := kapi.Get(context.Background(), key, nil)

		if err != nil {
			if etcdErr, ok := err.(etcd.Error); ok {
				switch etcdErr.Code {
				case etcd.ErrorCodeKeyNotFound:
					return LockerStateNone, nil
				default:
					// FIXME(xutao) "client: etcd cluster is unavailable or misconfigured"
					log.WithFields(logrus.Fields{
						"err":          err,
						"etcdErrCode":  etcdErr.Code,
						"retryCounter": retryCounter,
						"key":          key,
					}).Fatal("Fail to get key")
				}
			}

			if retryCounter >= 3 {
				// FIXME(xutao) "client: etcd cluster is unavailable or misconfigured"
				log.WithFields(logrus.Fields{
					"err":          err,
					"key":          key,
					"retryCounter": retryCounter,
				}).Fatal("Fail to get key")
			}
			time.Sleep(time.Second)
			retryCounter++
			continue
		}
		if resp.Node.Dir != false {
			log.WithFields(logrus.Fields{
				"key": key,
			}).Error("Etcd key is dir")
			err = fmt.Errorf("Etcd key is dir")
			return LockerStateError, err
		}
		if resp.Node.Value != value {
			return LockerStateUnlocked, nil
		}

		// everything is ok
		break
	}

	_, err = kapi.Delete(
		context.Background(),
		key,
		&etcd.DeleteOptions{PrevValue: self.hostname},
	)

	if err != nil {
		log.WithFields(logrus.Fields{
			"err": err,
			"key": key,
		}).Fatal("Fail to delete key")
	}
	self.DeleteContainerVip(containerName, ip)

	log.WithFields(logrus.Fields{
		"key":   key,
		"value": value,
	}).Info("Success to unlock key")
	return LockerStateDeleted, nil
}

func (self *Server) RemoveVirtualIpLock(lockedIp LockedIp) (state int, err error) {
	ip := lockedIp.ip
	key := fmt.Sprintf("%s/%s.lock", EtcdNetworkdVirtualIpKey, ip)
	kapi := etcd.NewKeysAPI(*self.etcd)

	resp, err := kapi.Delete(
		context.Background(),
		key,
		&etcd.DeleteOptions{PrevIndex: lockedIp.modifiedIndex},
	)

	if err != nil {
		log.WithFields(logrus.Fields{
			"err": err,
			"key": key,
		}).Error("Fail to remove key")
		return LockerStateLocked, nil
	}

	log.WithFields(logrus.Fields{
		"key":   key,
		"value": resp.PrevNode.Value,
	}).Debug("Success to remove key")
	return LockerStateDeleted, nil
}

func (self *Server) RunDnsmasq() {
	log.Info("Run dnsmasq")
	go self.dnsmasq.RunDnsmasqd()
}

func (self *Server) StopDnsmasq() {
	log.Debug("Stop dnsmasq")
	self.dnsmasq.StopDnsmasqd()
}

func (self *Server) RunAcl() {
	log.Info("Run Acl")
	go self.acl.RunAcl()
}

func (self *Server) StopAcl() {
	log.Info("Stop Acl")
	self.acl.StopAcl()
}

func (self *Server) RunWebrouter() {
	if self.webrouterIsRunning {
		return
	}
	log.Info("Run webrouter")
	self.webrouterIsRunning = true
	stopCh := make(chan struct{})
	eventCh := self.WatchWebrouterIps(stopCh)
	go func() {
		self.wg.Add(1)
		defer func() {
			log.Info("webrouter done")
			self.wg.Done()
		}()
		defer close(stopCh)
		for {
			select {
			case <-eventCh:
				self.ApplyWebrouterIps()
			case <-self.webrouterStopCh:
				stopCh <- struct{}{}
				return
			}
		}
	}()
}

func (self *Server) StopWebrouter() {
	if self.webrouterIsRunning {
		log.Debug("Stop webrouter")
		self.webrouterIsRunning = false
		self.webrouterStopCh <- struct{}{}
	}
}

func (self *Server) RunSwarm() {
	if self.swarmIsRunning {
		return
	}
	log.Info("Run swarm")
	self.swarmIsRunning = true
	stopCh := make(chan struct{})
	eventCh := self.WatchSwarmIps(stopCh)
	go func() {
		self.wg.Add(1)
		defer func() {
			log.Info("swarm done")
			self.wg.Done()
		}()
		defer close(stopCh)
		for {
			select {
			case <-eventCh:
				self.ApplySwarmIps()
			case <-self.swarmStopCh:
				stopCh <- struct{}{}
				return
			}
		}
	}()
}

func (self *Server) StopSwarm() {
	if self.swarmIsRunning {
		log.Debug("Stop swarm")
		self.swarmIsRunning = false
		self.swarmStopCh <- struct{}{}
	}
}

func (self *Server) RunDeployd() {
	if self.deploydIsRunning {
		return
	}
	log.Info("Run deployd")
	self.deploydIsRunning = true
	stopCh := make(chan struct{})
	eventCh := self.WatchDeploydIps(stopCh)
	go func() {
		self.wg.Add(1)
		defer func() {
			log.Info("deployd done")
			self.wg.Done()
		}()
		defer close(stopCh)
		for {
			select {
			case <-eventCh:
				self.ApplyDeploydIps()
			case <-self.deploydStopCh:
				stopCh <- struct{}{}
				return
			}
		}
	}()
}

func (self *Server) StopDeployd() {
	if self.deploydIsRunning {
		log.Debug("Stop deployd")
		self.deploydIsRunning = false
		self.deploydStopCh <- struct{}{}
	}
}

func (self *Server) RunLainlet() {
	log.Info("Run lainlet begin")
	self.InitVirtualIpDb()
	self.InitContainerDb()
	tickCh := time.NewTicker(time.Second * 5).C

	go self.WatchNetworkdVirtualIps()
	go self.WatchLainletVirtualIps()
	go func() {
		self.wg.Add(1)
		defer func() {
			log.Info("lainlet done")
			self.wg.Done()
		}()
		for {
			select {
			case <-self.eventCh:
				log.Debug("Received container event")
				self.FetchContainers()
				self.GcContainers()
				self.ApplyVirtualIps()
				self.GcVirtualIps()
				self.ApplyTinyDnsVips()
			case <-tickCh:
				self.FetchContainers()
				self.GcContainers()
				self.ApplyVirtualIps()
				self.GcVirtualIps()
			case <-self.lainletStopCh:
				return
			}
		}
	}()
}

func (self *Server) StopLainlet() {
	log.Debug("Stop")
	self.lainletStopCh <- struct{}{}
}

func (self *Server) Run() {
	self.healthIsRunning = false
	self.electIsRunning = false
	self.InitEventChan()
	self.RunElect()
	if self.dnsmasqFlag {
		self.RunDnsmasq()
	}
	if self.aclFlag {
		self.RunAcl()
	}
	self.RunLainlet()
	self.RunSignal()

	log.Debug("Wait stop signal")
	<-self.stopCh
	log.Debug("Wait goroutine exit")
	self.wg.Wait()
}

func (self *Server) Stop() {
	log.Info("Stop networkd")
	if self.swarmFlag {
		self.StopSwarm()
	}
	if self.tinydnsFlag {
		self.StopTinydns()
	}
	if self.dnsmasqFlag {
		self.StopDnsmasq()
	}
	if self.aclFlag {
		self.StopAcl()
	}
	if self.webrouterFlag {
		self.StopWebrouter()
	}
	if self.streamrouterFlag {
		self.StopStreamrouter()
	}
	if self.deploydFlag {
		self.StopDeployd()
	}
	if self.resolvConfFlag {
		self.StopResolvConf()
	}

	self.StopHealth()
	self.StopElect()
	self.StopLainlet()
	close(self.stopCh)
}
