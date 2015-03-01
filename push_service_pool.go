package apns

import (
	"container/list"
	"sync/atomic"
	"time"
)

var pushPool *pushServicePool

func initPushService() {
	pushPool = &pushServicePool{}
	pushPool.poolSize = defaultConfig.MinPoolSize

	pushPool.services = &pushServiceList{list.New()}
	pushPool.releasedServices = make(chan *pushService, defaultConfig.MaxPoolSize)
	pushPool.reportQueue = make(chan *notifReport, defaultConfig.MinPoolSize/2)
	pushPool.reportCount = &reportCount{}

	for i := uint(0); i < pushPool.poolSize; i++ {
		err := pushPool.services.New()
		if err != nil {
			logerr("Unable to start connection on init %s", err)
			continue
		}
	}

	go pushPool.monitor()
	logdebug("Connectio pool inited with %d connections from %d", pushPool.services.Len(), pushPool.poolSize)
}

type pushServicePool struct {
	poolSize         uint
	services         *pushServiceList
	releasedServices chan *pushService

	reportQueue chan *notifReport
	reportCount *reportCount

	upgradePoolCount   int
	downgradePoolCount int
	needToRemoveOne    bool
	lastFaildRetry     time.Time
}

func (psp *pushServicePool) monitor() {
	checker := time.Tick(time.Second * 1)
	reportTiker := time.Tick(defaultConfig.ReportingInterval)
	for {
		select {
		case <-reportTiker:
			psp.report()
		case report := <-psp.reportQueue:
			psp.addReport(report)
		case <-checker:
			createNewConnection := false

			upgrade, downgrade := psp.checkScale()

			if upgrade {
				psp.poolSize++
				createNewConnection = true
				logwarn("Push service pool size will upgrade to: %d", psp.poolSize)
			} else if downgrade {
				psp.services.Remove(nil)
				psp.poolSize--
				logwarn("Push service pool size will downgrade to: %d", psp.poolSize)
			}

			if !createNewConnection && psp.recreateFailed() {
				createNewConnection = true
				loginfo("A failed push service is going to be revived")
			}

			if createNewConnection {
				err := psp.services.New()
				if err != nil {
					logerr("Unable to start a new apple connection: %s", err)
					break
				}
			}
		case service := <-psp.releasedServices:
			if service.lastError != nil {
				psp.services.Remove(service)
				logwarn("I have a failed push service, it will be removed from pool (%s)", service.lastError)
				break
			}
		}
	}
}

func (psp *pushServicePool) recreateFailed() bool {
	if psp.services.Len() >= psp.poolSize {
		return false
	}

	if time.Since(psp.lastFaildRetry) >= defaultConfig.ReconnectDistanceTime {
		psp.lastFaildRetry = time.Now()
		return true
	}

	return false
}

func (psp *pushServicePool) checkScale() (upgrade, downgrade bool) {
	messageQueueSize := atomic.LoadInt64(&messagesInQueue)
	if messageQueueSize > int64(defaultConfig.UpgradePool) {
		psp.upgradePoolCount++
	} else {
		psp.upgradePoolCount = 0
	}

	if messageQueueSize < int64(defaultConfig.DowngradePool) {
		psp.downgradePoolCount++
	} else {
		psp.downgradePoolCount = 0
	}

	if psp.upgradePoolCount >= 10 { //10 seconds
		if psp.poolSize >= defaultConfig.MaxPoolSize {
			logwarn("Upgrade pool requested but i'm in max pool size (messages: %d, pool size: %d)  ",
				messageQueueSize, psp.poolSize)
			return false, false
		}
		return true, false
	}

	if psp.downgradePoolCount >= 300 { //5 minutes
		if psp.poolSize <= defaultConfig.MinPoolSize {
			return false, false
		}
		return false, true
	}

	return false, false
}

type notifReport struct {
	sent        int64
	resent      int64
	success     int64
	failed      int64
	confirmTime time.Duration
	sendingTime time.Duration
}

type reportCount struct {
	totalSent        int64
	totalResent      int64
	successConfirmed int64
	failedConfirmed  int64
	confirmTime      time.Duration
	sendingTime      time.Duration
}

func (psp *pushServicePool) report() {
	format := "APNs Report - ActivePool: %d, PoolSize: %d, Queue: %d, Sent: %d, ReSent: %d, Confirmed: %d, ConfirmedFailed: %d, ConfirmTimeAvg: %s, SendingTime: %s"
	queueSize := atomic.LoadInt64(&messagesInQueue)

	rc := psp.reportCount

	var (
		confirmTimeAvg time.Duration = 0
		sendingTimeAvg time.Duration = 0
	)

	if rc.successConfirmed+rc.failedConfirmed > 0 {
		confirmTimeAvg = rc.confirmTime / time.Duration(rc.successConfirmed+rc.failedConfirmed)
		sendingTimeAvg = rc.confirmTime / time.Duration(rc.successConfirmed+rc.failedConfirmed)
	}

	loginfo(format, psp.services.Len(), psp.poolSize, queueSize, rc.totalSent, rc.totalResent, rc.successConfirmed, rc.failedConfirmed, confirmTimeAvg, sendingTimeAvg)
	psp.reportCount = &reportCount{}
}

func (psp *pushServicePool) addReport(report *notifReport) {
	rc := psp.reportCount
	rc.totalSent += report.sent
	rc.totalResent += report.resent
	rc.successConfirmed += report.success
	rc.failedConfirmed += report.failed
	rc.confirmTime += report.confirmTime
	rc.sendingTime += report.sendingTime
}

type pushServiceList struct {
	psList *list.List
}

func (psl *pushServiceList) New() error {
	ps, err := newPushService()
	if err != nil {
		return err
	}

	psl.psList.PushBack(ps)
	return nil
}

func (psl *pushServiceList) Remove(s *pushService) {
	var toRemove *list.Element
	if s == nil {
		toRemove = psl.psList.Front()
	} else {
		for e := psl.psList.Front(); e != nil; e = e.Next() {
			sp := e.Value.(*pushService)
			if sp == s {
				toRemove = e
				break
			}
		}
	}
	if toRemove == nil {
		logerr("I wanted to remove a push service and i didn't find it, this is a bug")
		return
	}

	toRemove.Value.(*pushService).destroy()
	psl.psList.Remove(toRemove)
}

func (psl *pushServiceList) Len() uint {
	return uint(psl.psList.Len())
}
