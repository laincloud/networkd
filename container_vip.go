package main

import (
	"errors"
	"fmt"
	"time"

	"github.com/Sirupsen/logrus"
	"github.com/coreos/etcd/Godeps/_workspace/src/golang.org/x/net/context"
	etcd "github.com/coreos/etcd/client"
)

const MAX = 1000000
const VipLockTTL = 30

func ProcVipsHealthy(appName string, procName string) bool {
	// every container vip is balanced TODO
	return false
}

func (self *Server) isContainerVipBalanced(containerName string, added int) bool {
	container, ok := self.cDb.Get(containerName)
	if !ok {
		log.WithFields(logrus.Fields{
			"containerName": container.appName,
		}).Fatal("can not find container")
	}
	key := fmt.Sprintf("%s/%s/%s", EtcdNetworkdContainerVips, container.appName, container.procName)
	kapi := etcd.NewKeysAPI(*self.etcd)
	retryCounter := 0
	for {
		resp, err := kapi.Get(context.Background(), key, &etcd.GetOptions{Recursive: true})
		if err != nil {
			retryCounter += 1
		} else {
			vipUsed := make([]int, len(resp.Node.Nodes))
			minUsed := MAX
			for i, containerVips := range resp.Node.Nodes {
				if len(containerVips.Nodes) == 0 {
					continue //TODO gc proc
				}
				vipUsed[i] = getTotalUsedVips(containerVips)
				if vipUsed[i] < minUsed {
					minUsed = vipUsed[i]
				}
			}
			if vipUsed[container.instance-1]+added > minUsed+1 {
				return false
			}
			return true
		}
		if retryCounter > 3 {
			log.Fatal("fail to get vips of containers")
		}
		time.Sleep(time.Second)
	}

	return false
}

func getTotalUsedVips(containerVips *etcd.Node) int {
	count := 0
	for _, vip := range containerVips.Nodes {
		if vip.Value == VirtualIpState[VirtualIpStateUsed] {
			count++
		}
	}
	log.Debug("count is ", count)
	return count
}

func (self *Server) setContainerVipStatus(containerName string, ip string, status int, isPrevExist bool) error {
	container, ok := self.cDb.Get(containerName)
	if !ok {
		log.WithFields(logrus.Fields{
			"containerName": containerName,
			"ip":            ip,
		}).Error("container not found")
		return errors.New("container not found")
	}
	key := fmt.Sprintf("%s/%s/%s/%d/%s", EtcdNetworkdContainerVips, container.appName, container.procName, container.instance, ip)
	kapi := etcd.NewKeysAPI(*self.etcd)
	retryCounter := 0
	for {
		prev := etcd.PrevExist
		if !isPrevExist {
			prev = etcd.PrevNoExist
		}
		resp, err := kapi.Set(context.Background(), key, VirtualIpState[status], &etcd.SetOptions{
			PrevExist: prev,
			TTL:       time.Second * VipLockTTL,
		})
		if err != nil {
			if etcdErr, ok := err.(etcd.Error); ok {
				log.WithFields(logrus.Fields{
					"err":          err,
					"etcdErrCode":  etcdErr.Code,
					"retryCounter": retryCounter,
					"key":          key,
					"response":     resp,
				}).Debug("Fail to set key, for debug")
				if (etcdErr.Code == etcd.ErrorCodeNodeExist && !isPrevExist) || (etcdErr.Code == etcd.ErrorCodeKeyNotFound && isPrevExist) {
					break
				}
			} else {
				log.WithFields(logrus.Fields{
					"err":  err,
					"type": err,
				}).Error("fail to set key ...")
			}
			if retryCounter >= 3 {
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
		break
	}
	return nil
}

func (self *Server) AddContainerVip(containerName string, ip string) {
	//container is alive and use the vip
	self.setContainerVipStatus(containerName, ip, VirtualIpStateUsed, true)
}

func (self *Server) DeleteContainerVip(containerName string, ip string) {
	//container is alive but not use the vip
	self.setContainerVipStatus(containerName, ip, VirtualIpStateUnused, true)
}

func (self *Server) CreateContainerVipKey(containerName string, ip string) {
	// container is alive and wait to use the vip
	self.setContainerVipStatus(containerName, ip, VirtualIpStateUnused, false)
}

func (self *Server) DeleteContainerVipKey(containerName string, ip string) {
	// container is not alive
	container, ok := self.cDb.Get(containerName)
	if !ok {
		log.WithFields(logrus.Fields{
			"containerName": containerName,
			"ip":            ip,
		}).Error("container not found")
	}
	key := fmt.Sprintf("%s/%s/%s/%d/%s", EtcdNetworkdContainerVips, container.appName, container.procName, container.instance, ip)
	kapi := etcd.NewKeysAPI(*self.etcd)
	_, err := kapi.Delete(context.Background(), key, nil)
	if err != nil {
		log.WithFields(logrus.Fields{
			"err": err,
			"key": ip,
		}).Error("fail to delete key")
	}
}

func (self *Server) DeleteContainerKey(containerName string) {
	//TODO
}
