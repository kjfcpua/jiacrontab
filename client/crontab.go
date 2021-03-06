package main

import (
	"context"
	"fmt"
	"jiacrontab/client/store"
	"jiacrontab/libs"
	"jiacrontab/libs/proto"
	"log"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

func newCrontab(taskChanSize int) *crontab {
	return &crontab{
		taskChan:     make(chan *proto.TaskArgs, taskChanSize),
		delTaskChan:  make(chan *proto.TaskArgs, taskChanSize),
		killTaskChan: make(chan *proto.TaskArgs, taskChanSize),
		handleMap:    make(map[string]*handle),
	}
}

type handle struct {
	cancel         context.CancelFunc
	cancelCmdArray []context.CancelFunc
	clockChan      chan time.Time
	timeout        int64
}

type crontab struct {
	taskChan     chan *proto.TaskArgs
	delTaskChan  chan *proto.TaskArgs
	killTaskChan chan *proto.TaskArgs
	handleMap    map[string]*handle
	lock         sync.RWMutex
}

func (c *crontab) add(t *proto.TaskArgs) {
	c.taskChan <- t
	log.Printf("add task %+v", *t)
}

func (c *crontab) quickStart(t *proto.TaskArgs, content *[]byte) {
	start := time.Now().Unix()
	args := strings.Split(t.Args, " ")
	t.LastExecTime = start
	var timeout int64
	if t.Timeout == 0 {
		timeout = 60
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(timeout)*time.Second)
	err := execScript(ctx, fmt.Sprintf("%s-%s.log", t.Name, t.Id), t.Command, globalConfig.logPath, content, args...)
	cancel()

	if err != nil {
		*content = append(*content, []byte(err.Error())...)
	}

	t.LastCostTime = time.Now().Unix() - start
	globalStore.Sync()

	log.Printf("%s:  quic start end costTime %ds %v", t.Name, t.LastCostTime, err)

}

func (c *crontab) stop(t *proto.TaskArgs) {
	c.kill(t)
	c.delTaskChan <- t
	log.Println("stop", t.Name, t.Id)
}

func (c *crontab) kill(t *proto.TaskArgs) {
	c.killTaskChan <- t
	log.Println("kill", t.Name, t.Id)
}

func (c *crontab) delete(t *proto.TaskArgs) {
	globalStore.Update(func(s *store.Store) {
		delete(s.TaskList, t.Id)
	})
	c.kill(t)
	c.delTaskChan <- t
	log.Println("delete", t.Name, t.Id)
}

func (c *crontab) ids() []string {
	var sli []string
	c.lock.Lock()
	for k, _ := range c.handleMap {
		sli = append(sli, k)
	}

	c.lock.Unlock()
	return sli
}

func (c *crontab) run() {
	// initialize
	go func() {
		globalStore.Update(func(s *store.Store) {
			for _, v := range s.TaskList {
				if v.State != 0 {

					c.add(v)
				}
			}
		}).Sync()

	}()
	// global clock
	go func() {
		t := time.Tick(1 * time.Minute)
		for {
			now := <-t

			// broadcast
			c.lock.Lock()
			for _, v := range c.handleMap {
				v.clockChan <- now
			}
			c.lock.Unlock()
		}
	}()

	// add task
	go func() {
		for {
			select {
			case task := <-c.taskChan:
				ctx, cancel := context.WithCancel(context.Background())
				task.State = 1
				c.lock.Lock()
				c.handleMap[task.Id] = &handle{
					cancel:    cancel,
					clockChan: make(chan time.Time),
				}
				c.lock.Unlock()

				go c.deal(task, ctx)
			}
		}
	}()
	// remove task
	go func() {
		for {
			select {
			case task := <-c.delTaskChan:
				c.lock.Lock()
				if handle, ok := c.handleMap[task.Id]; ok {
					handle.cancel()
				}
				c.lock.Unlock()
				task.State = 0
			}
		}
	}()

	// kill task
	go func() {
		for {
			select {
			case task := <-c.killTaskChan:
				c.lock.Lock()
				if handle, ok := c.handleMap[task.Id]; ok {
					if handle.cancelCmdArray != nil {
						for _, cancel := range handle.cancelCmdArray {
							cancel()
						}
						handle.cancelCmdArray = make([]context.CancelFunc, 0)
						c.lock.Unlock()
						task.State = 1
						globalStore.Sync()
					} else {
						c.lock.Unlock()
					}
				} else {
					c.lock.Unlock()
				}
			}
		}
	}()

}

func (c *crontab) deal(task *proto.TaskArgs, ctx context.Context) {
	var wgroup sync.WaitGroup
	for {
		c.lock.Lock()
		h := c.handleMap[task.Id]
		c.lock.Unlock()
		select {
		case now := <-h.clockChan:
			wgroup.Add(1)
			go func() {
				defer func() {
					libs.MRecover()
					wgroup.Done()
				}()

				check := task.C
				if checkMonth(check, now.Month()) &&
					checkWeekday(check, now.Weekday()) &&
					checkDay(check, now.Day()) &&
					checkHour(check, now.Hour()) &&
					checkMinute(check, now.Minute()) {

					var content []byte
					var hdl *handle

					now2 := time.Now()
					start := now2.UnixNano()
					args := strings.Split(task.Args, " ")
					task.LastExecTime = now2.Unix()
					flag := true
					task.State = 2
					ctx, cancel := context.WithCancel(context.Background())

					c.lock.Lock()
					hdl = c.handleMap[task.Id]
					if len(hdl.cancelCmdArray) >= task.MaxConcurrent {
						hdl.cancelCmdArray[0]()
						hdl.cancelCmdArray = hdl.cancelCmdArray[1:]
					}
					hdl.cancelCmdArray = append(hdl.cancelCmdArray, cancel)

					c.lock.Unlock()

					if task.Timeout != 0 {
						time.AfterFunc(time.Duration(task.Timeout)*time.Second, func() {
							if flag {
								switch task.OpTimeout {
								case "email":
									sendMail(task.MailTo, globalConfig.addr+"提醒脚本执行超时", fmt.Sprintf(
										"任务名：%s\n详情：%s %v\n开始时间：%s\n超时：%ds",
										task.Name, task.Command, task.Args, now2.Format("2006-01-02 15:04:05"), task.Timeout))
								case "kill":
									cancel()

								case "email_and_kill":
									cancel()
									sendMail(task.MailTo, globalConfig.addr+"提醒脚本执行超时", fmt.Sprintf(
										"任务名：%s\n详情：%s %v\n开始时间：%s\n超时：%ds",
										task.Name, task.Command, task.Args, now2.Format("2006-01-02 15:04:05"), task.Timeout))
								case "ignore":
								default:
								}
							}

						})
					}
					atomic.AddInt32(&task.NumberProcess, 1)
					err := execScript(ctx, fmt.Sprintf("%s-%s.log", task.Name, task.Id), task.Command, globalConfig.logPath, &content, args...)
					flag = false
					task.LastCostTime = time.Now().UnixNano() - start

					atomic.AddInt32(&task.NumberProcess, -1)
					if task.NumberProcess == 0 {
						task.State = 1
					} else {
						task.State = 2
					}
					globalStore.Sync()

					log.Printf("%s:%s %v %s %.3fs %v", task.Name, task.Command, task.Args, task.OpTimeout, float64(task.LastCostTime)/1000000000, err)

				}
			}()
		case <-ctx.Done():
			// 等待所有的计划任务执行完毕
			wgroup.Wait()
			task.State = 0
			c.lock.Lock()
			close(c.handleMap[task.Id].clockChan)
			delete(c.handleMap, task.Id)
			c.lock.Unlock()
			globalStore.Sync()
			return
		}

	}

}
