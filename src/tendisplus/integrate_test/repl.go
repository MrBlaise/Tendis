// Copyright (C) 2020 THL A29 Limited, a Tencent company.  All rights reserved.
// Please refer to the license text that comes with this tendis open source
// project for additional information.

package main

import (
	"flag"
	"fmt"
	"github.com/mediocregopher/radix.v2/redis"
	"github.com/ngaut/log"
	"math/rand"
	"sync"
	"sync/atomic"
	"tendisplus/integrate_test/util"
	"time"
)

var (
	mport       = flag.Int("masterport", 62001, "master port")
	sport       = flag.Int("slaveport", 62002, "slave port")
	zsetcount   = flag.Int("zsetcount", 1, "zset count")
	setcount    = flag.Int("setcount", 100, "set count")
	listcount   = flag.Int("listcount", 100, "list count")
	hashcount   = flag.Int("hashcount", 100, "hash count")
	kvcount     = flag.Int("kvcount", 100000, "kv count")
	scriptcount = flag.Int("scriptcount", 100000, "script count")
	threadnum   = flag.Int("threadnum", 4, "thd count")
	loadsecs    = flag.Int("loadsecs", 20, "seconds for loading data")
)

type TendisType int

const (
	KV TendisType = iota
	SET
	ZSET
	LIST
	HASH
	SCRIPT
)

type Record struct {
	Pk   string
	Sk   string
	Type TendisType
}

func getRandomType() TendisType {
	typelist := []TendisType{KV, SET, ZSET, SCRIPT}
	return typelist[rand.Int()%len(typelist)]
}

func testReplMatch2(kvstore_count int, m *util.RedisServer, s *util.RedisServer) {
	var wg sync.WaitGroup
	running := int32(1)
	buf := make(chan *Record)
	keys := []*Record{}
	for i := 0; i < *threadnum; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			cli := createClient(m)
			for atomic.LoadInt32(&running) == 1 {
				tp := getRandomType()
				if tp == KV {
					suffix := rand.Int() % (*kvcount)
					pk := fmt.Sprintf("kv%d", suffix)
					r, err := cli.Cmd("set", pk, fmt.Sprintf("%d", suffix)).Str()
					if err != nil {
						log.Fatalf("do set failed:%v", err)
					} else if r != "OK" {
						log.Fatalf("do set error:%s", r)
					}
					buf <- &Record{Pk: pk, Sk: "", Type: tp}
				} else if tp == SET {
					suffix := rand.Int() % (*setcount)
					pk := fmt.Sprintf("set%d", suffix)
					sk := fmt.Sprintf("sk%d", rand.Int()%10000)
					_, err := cli.Cmd("sadd", pk, sk).Int()
					if err != nil {
						log.Fatalf("do sadd failed:%v", err)
					}
					buf <- &Record{Pk: pk, Sk: sk, Type: tp}
				} else if tp == ZSET {
					suffix := rand.Int() % (*zsetcount)
					pk := fmt.Sprintf("zset%d", suffix)
					sk := fmt.Sprintf("sk%d", rand.Int()%20000000)
					_, err := cli.Cmd("zrem", pk, sk).Int()
					if err != nil {
						log.Fatalf("do zrem %s %s failed:%v", pk, sk, err)
					}
					_, err = cli.Cmd("zadd", pk, rand.Int()%10000000, sk).Int()
					if err != nil {
						log.Fatalf("do zadd %s %s failed:%v,%d", pk, sk, err)
					}
					buf <- &Record{Pk: pk, Sk: sk, Type: tp}
				} else if tp == SCRIPT {
					suffix := rand.Int() % (*scriptcount)
					pk := fmt.Sprintf("return KEYS[%d]", suffix)
					sk, err := cli.Cmd("script", "load", pk).Str()
					if err != nil {
						log.Fatalf("load script error:%v", err)
					} else if len(sk) != 40 {
						log.Fatalf("script sha code has wrong size")
					}
					buf <- &Record{Pk: pk, Sk: sk, Type: tp}
				}
			}
		}(i)
	}

	var wg1 sync.WaitGroup
	go func() {
		wg1.Add(1)
		defer wg1.Done()
		for o := range buf {
			keys = append(keys, o)
		}
	}()

	time.Sleep(time.Duration(*loadsecs) * time.Second)
	atomic.StoreInt32(&running, 0)
	log.Infof("close all producers")
	wg.Wait()
	log.Infof("all producers closed")
	close(buf)
	log.Infof("queue closed")
	wg1.Wait()
	log.Infof("consumer closed")

	cli1 := createClient(m)
	cli2 := createClient(s)

	for i := 0; i < kvstore_count; i++ {
		mPos, err := cli1.Cmd("binlogpos", fmt.Sprintf("%d", i)).Int64()
		if err != nil {
			log.Fatalf("binlogpos of master failed:%v", err)
		}
		for {
			sPos, err := cli2.Cmd("binlogpos", fmt.Sprintf("%d", i)).Int64()
			if err != nil {
				log.Fatalf("binlogpos of slave failed:%v", err)
			}
			if sPos == mPos {
				log.Infof("m/s binlog matches")
				break
			} else {
				log.Infof("mpos:%d, spos:%d", mPos, sPos)
				time.Sleep(1 * time.Second)
			}
		}
	}
	for _, o := range keys {
		//log.Infof("%s", o.Pk)
		if o.Type == KV {
			r, err := cli2.Cmd("get", o.Pk).Str()
			if err != nil {
				log.Fatalf("do get on slave failed:%v, get %s", err, o.Pk)
			}
			r1, err := cli1.Cmd("get", o.Pk).Str()
			if err != nil {
				log.Fatalf("do get on master failed:%v, get %s", err, o.Pk)
			}
			if r != r1 {
				log.Fatalf("kv:%s not match", o.Pk)
			}
		} else if o.Type == SET {
			r, err := cli2.Cmd("sismember", o.Pk, o.Sk).Int()
			if err != nil {
				log.Fatalf("do sismember on slave failed:%s,%s,%v", o.Pk, o.Sk, err)
			}
			r1, err := cli1.Cmd("sismember", o.Pk, o.Sk).Int()
			if err != nil {
				log.Fatalf("do sismember on master failed:%v", err)
			}
			if r != r1 {
				log.Fatalf("sismember:%s,%s not match", o, o.Pk, o.Sk)
			}
		} else if o.Type == ZSET {
			r, err := cli2.Cmd("zscore", o.Pk, o.Sk).Str()
			if err != nil {
				log.Fatalf("do zscore on slave failed:%v, zscore %s %s", err, o.Pk, o.Sk)
			}
			r1, err := cli1.Cmd("zscore", o.Pk, o.Sk).Str()
			if err != nil {
				log.Fatalf("do zscore on master failed:%v, zscore %s %s", err, o.Pk, o.Sk)
			}
			if r != r1 {
				log.Fatalf("zscore:%s,%s not match", o.Pk, o.Sk)
			}
		} else if o.Type == SCRIPT {
			r, err := cli2.Cmd("script", "exists", o.Sk).Array()
			if err != nil {
				log.Fatalf("do script exists on slave failed:%v", err)
			}
			r0, err := r[0].Int()
			if err != nil {
				log.Fatalf("do script exists on slave failed:%v", err)
			}
			r1, err := cli1.Cmd("script", "exists", o.Sk).Array()
			if err != nil {
				log.Fatalf("do script exists on master failed:%v", err)
			}
			r10, err := r1[0].Int()
			if err != nil {
				log.Fatalf("do script exists on master failed:%v", err)
			}
			if r0 != r10 {
				log.Fatalf("script:%s,%s not match", o.Pk, o.Sk)
			}
		}
	}
	log.Infof("compare complete")
}

func testReplMatch1(kvstore_count int, m *util.RedisServer, s *util.RedisServer) {
	cli1 := createClient(m)
	cli2 := createClient(s)

	if r, err := cli2.Cmd("slaveof", "127.0.0.1", fmt.Sprintf("%d", m.Port)).Str(); err != nil {
		log.Fatalf("do slaveof failed:%v", err)
	} else if r != "OK" {
		log.Fatalf("do slaveof error:%s", r)
	}

	for i := 0; i < 10000; i += 1 {
		r, err := cli1.Cmd("set", fmt.Sprintf("%d", i), fmt.Sprintf("%d", i)).Str()
		if err != nil {
			log.Fatalf("do set on master failed:%v", err)
		} else if r != "OK" {
			log.Fatalf("do set on master error:%s", r)
		}
	}
	time.Sleep(10 * time.Second)
	for i := 0; i < kvstore_count; i++ {
		mPos, err := cli1.Cmd("binlogpos", i).Int64()
		if err != nil {
			log.Fatalf("binlogpos of master store %d failed:%v", i, err)
		}
		for {
			sPos, err := cli2.Cmd("binlogpos", i).Int64()
			if err != nil {
				log.Fatalf("binlogpos of slave store %d failed:%v", i, err)
			}
			if sPos == mPos {
				log.Infof("store %d m/s binlog matches", i)
				break
			} else {
				log.Infof("store %d, mpos:%d, spos:%d", i, mPos, sPos)
				time.Sleep(1 * time.Second)
			}
		}
	}

	for i := 0; i < 10000; i += 1 {
		r, err := cli2.Cmd("get", fmt.Sprintf("%d", i)).Str()
		if err != nil {
			log.Fatalf("do get on slave failed:%v", err)
		} else if r != fmt.Sprintf("%d", i) {
			log.Fatalf("do get on slave bad return:%s", r)
		}
	}
}

func testRepl(m_port int, s_port int, kvstore_count int) {
	m := util.RedisServer{}
	s := util.RedisServer{}
	pwd := util.GetCurrentDirectory()
	log.Infof("current pwd:" + pwd)

	cfgArgs := make(map[string]string)
	cfgArgs["requirepass"] = "tendis+test"
	cfgArgs["masterauth"] = "tendis+test"

	m_port = util.FindAvailablePort(m_port)
	log.Infof("FindAvailablePort:%d", m_port)
	m.Init("127.0.0.1", m_port, pwd, "m_")
	if err := m.Setup(false, &cfgArgs); err != nil {
		log.Fatalf("setup master failed:%v", err)
	}
	s_port = util.FindAvailablePort(s_port)
	log.Infof("FindAvailablePort:%d", s_port)
	s.Init("127.0.0.1", s_port, pwd, "s_")
	if err := s.Setup(false, &cfgArgs); err != nil {
		log.Fatalf("setup slave failed:%v", err)
	}
	time.Sleep(15 * time.Second)

	testReplMatch1(kvstore_count, &m, &s)
	testReplMatch2(kvstore_count, &m, &s)

	shutdownServer(&s, *shutdown, *clear)
	shutdownServer(&m, *shutdown, *clear)
}

func testBindMultiIP(m_port int, kvstore_count int) {
	m := util.RedisServer{}
	pwd := util.GetCurrentDirectory()
	log.Infof("current pwd:" + pwd)

	cfgArgs := make(map[string]string)
	ip := "127.0.0.1"
	ip2 := util.GetIp()
	log.Infof("getIp:%v", ip2)
	cfgArgs["bind"] = ip
	cfgArgs["bind2"] = ip2
	cfgArgs["requirepass"] = "tendis+test"
	cfgArgs["masterauth"] = "tendis+test"

	m_port = util.FindAvailablePort(m_port)
	log.Infof("FindAvailablePort:%d", m_port)
	m.Init("127.0.0.1", m_port, pwd, "m_")
	if err := m.Setup(false, &cfgArgs); err != nil {
		log.Fatalf("setup master failed:%v", err)
	}
	time.Sleep(3 * time.Second)

	cli1, err := redis.DialTimeout("tcp", fmt.Sprintf("%s:%d", ip, m.Port), 10*time.Second)
	if err != nil {
		log.Fatalf("can't connect to %s %d: %v", ip, m.Port, err)
	}
	if *auth != "" {
		if v, err := cli1.Cmd("AUTH", *auth).Str(); err != nil || v != "OK" {
			log.Fatalf("auth result:%s failed:%v. %s:%d auth:%s", v, err, ip, m.Port, *auth)
		}
	}
	cli2, err := redis.DialTimeout("tcp", fmt.Sprintf("%s:%d", ip2, m.Port), 10*time.Second)
	if err != nil {
		log.Fatalf("can't connect to %s %d: %v", ip2, m.Port, err)
	}
	if *auth != "" {
		if v, err := cli2.Cmd("AUTH", *auth).Str(); err != nil || v != "OK" {
			log.Fatalf("auth result:%s failed:%v. %s:%d auth:%s", v, err, ip2, m.Port, *auth)
		}
	}

	if r, err := cli1.Cmd("set", "a", "1").Str(); err != nil {
		log.Fatalf("ip cmd failed:%v", err)
	} else if r != "OK" {
		log.Fatalf("ip cmd error:%s", r)
	}

	if r, err := cli2.Cmd("set", "b", "2").Str(); err != nil {
		log.Fatalf("ip2 cmd failed:%v", err)
	} else if r != "OK" {
		log.Fatalf("ip2 cmd error:%s", r)
	}

	if r, err := cli1.Cmd("get", "b").Str(); err != nil {
		log.Fatalf("ip cmd failed:%v", err)
	} else if r != "2" {
		log.Fatalf("ip cmd error:%s", r)
	}
	if r, err := cli2.Cmd("get", "a").Str(); err != nil {
		log.Fatalf("ip2 cmd failed:%v", err)
	} else if r != "1" {
		log.Fatalf("ip2 cmd error:%s", r)
	}

	shutdownServer(&m, *shutdown, *clear)
}

func main() {
	log.SetFlags(log.LstdFlags | log.Lmicroseconds | log.Lshortfile)
	flag.Parse()
	rand.Seed(time.Now().UnixNano())
	testRepl(*mport, *sport, *kvstorecount)
	testBindMultiIP(*mport+1, *kvstorecount)
	log.Infof("repl.go passed.")
}
