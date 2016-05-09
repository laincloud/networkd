package main

import (
	"fmt"
	"github.com/Sirupsen/logrus"
	"os"
	"os/signal"
	"syscall"
)

type signalHandler func(s os.Signal, arg interface{})

type signalSet struct {
	m map[os.Signal]signalHandler
}

func signalSetNew() *signalSet {
	ss := new(signalSet)
	ss.m = make(map[os.Signal]signalHandler)
	return ss
}

func (set *signalSet) register(s os.Signal, handler signalHandler) {
	if _, found := set.m[s]; !found {
		set.m[s] = handler
	}
}

func (set *signalSet) handle(sig os.Signal, arg interface{}) (err error) {
	if _, found := set.m[sig]; found {
		set.m[sig](sig, arg)
		return nil
	} else {
		return fmt.Errorf("No handler available for signal %v", sig)
	}
}

func (self *Server) RunSignal() {
	go func() {
		ss := signalSetNew()
		handler := func(s os.Signal, arg interface{}) {
			switch s {
			case syscall.SIGCHLD:
			case syscall.SIGUSR1:
			case syscall.SIGUSR2:
			default:
				log.WithFields(logrus.Fields{
					"sig": s,
				}).Info("Receive signal")
				self.Stop()
			}
		}

		ss.register(syscall.SIGINT, handler)
		ss.register(syscall.SIGUSR1, handler)
		ss.register(syscall.SIGUSR2, handler)
		ss.register(syscall.SIGCHLD, handler)
		ss.register(syscall.SIGTERM, handler)

		for {
			c := make(chan os.Signal)
			var sigs []os.Signal
			for sig := range ss.m {
				sigs = append(sigs, sig)
			}
			signal.Notify(c)
			sig := <-c

			err := ss.handle(sig, nil)
			if err != nil {
				log.WithFields(logrus.Fields{
					"sig": sig,
				}).Info("Receive unknown signal")
			}
		}
	}()
}
