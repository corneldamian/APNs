package apns

import (
	"bytes"
	"crypto/tls"
	"encoding/binary"
	"encoding/hex"
	"io"
	"net"
	"strings"
	"sync"
	"time"
)

// this method will start the feedback service and is returning
// an object with 3 methods Channel, Interval and Stop
// on Channel you will recevie all the feedback messages
// Interval check at each interval for new messages
// Interval is default 0, this will make it do only one request then will stop, if interval > 0 then at each interval will check for new message (the timer is started when you request the channel)
// after Stop make sure you read until channel close so you get all the messages
//
func Feedback() *feedback {
	if cachedFeedbackService != nil {
		return cachedFeedbackService
	}

	f := &feedback{}
	f.chanel = make(chan *FeedbackMessage, defaultConfig.MessageQueueSize)
	f.doStop = make(chan bool, 2)
	f.stopWait = &sync.WaitGroup{}

	logdebug("Feedback service inited")

	return f
}

var cachedFeedbackService *feedback

type FeedbackMessage struct {
	DeviceToken  string
	DiscoverTime time.Time
}

type feedback struct {
	chanel      chan *FeedbackMessage
	conn        *tls.Conn
	interval    time.Duration
	lastRunTime time.Time
	doStop      chan bool
	stopWait    *sync.WaitGroup
}

// when you request channel for the first time or if interval = 0 connection is created now
// if a request it's in progress or the interval > 0 (and is a second request) you will receive directly
// the channel
func (f *feedback) Channel() (<-chan *FeedbackMessage, error) {
	if f.conn != nil {
		return f.chanel, nil
	}

	if err := f.createConnection(); err != nil {
		logerr("Unable to start feedback connection: %s", err)
		return nil, err
	}

	f.stopWait.Add(1)
	go f.monitorService()

	return f.chanel, nil
}

func (f *feedback) Stop() {
	f.doStop <- true
	f.stopWait.Wait()
}

func (f *feedback) Interval(i time.Duration) {
	f.interval = i
}

func (f *feedback) monitorService() {
	defer f.stopWait.Done()
	logdebug("Montitor service started")

	check := time.Tick(time.Second * 1)
	for {
		select {
		case <-f.doStop:
			loginfo("Stopping feedback service")
			close(f.chanel)
			return
		case <-check:
			if time.Since(f.lastRunTime) >= f.interval {
				if f.conn == nil {
					if err := f.createConnection(); err != nil {
						logerr("Wanted to create a new connection to feedback, but i have error: %s", err)
						f.lastRunTime = time.Now().Add(-f.interval + time.Second*30) //reconect after 30s
						break
					}
				}

				f.readMessages()
				f.lastRunTime = time.Now()

				if f.interval == 0 {
					loginfo("This was a once request, i will close the service")
					f.conn = nil
					f.doStop <- true
				}
			}
		}
	}
}

func (f *feedback) readMessages() {
	newMessages := 0
	defer func() {
		loginfo("APNs Feedback report: %d new tokens received", newMessages)
	}()

	f.conn.SetReadDeadline(time.Now().Add(defaultConfig.FeedbackReadTimeout))
	logdebug("Reading new messages")

	binaryToken := make([]byte, 32, 32)
	tokenLen := uint16(0)
	buff := make([]byte, 38, 38)

	for {
		_, err := f.conn.Read(buff[:])
		if err != nil {
			if err != io.EOF {
				if neterr, ok := err.(net.Error); ok && neterr.Timeout() {
					logdebug("Feedback: No message received after read timeout")
					return
				}
				logwarn("Feedback service connection closed by apple: %s", err)
			}
			logdebug("Feedback: apple has closed the connection")
			return
		}

		fm := &FeedbackMessage{}
		resp := bytes.NewReader(buff)

		binary.Read(resp, binary.BigEndian, &fm.DiscoverTime)
		binary.Read(resp, binary.BigEndian, &tokenLen)
		binary.Read(resp, binary.BigEndian, &binaryToken)

		if tokenLen != 32 {
			logerr("Invalid token received, size is: %d but it should be 32", tokenLen)
		}

		fm.DeviceToken = hex.EncodeToString(binaryToken)

		f.chanel <- fm
	}
}

func (f *feedback) createConnection() error {
	conf, err := getFeedbackTlsConfig()
	if err != nil {
		return err
	}

	conn, err := tls.Dial("tcp", defaultConfig.feedback_gateway, conf)
	if err != nil {
		return err
	}

	if err := conn.Handshake(); err != nil {
		return err
	}

	f.conn = conn

	return nil
}

var cachedFeedbackTlsConfig *tls.Config

func getFeedbackTlsConfig() (cfg *tls.Config, err error) {
	if cachedFeedbackTlsConfig != nil {
		return cachedFeedbackTlsConfig, nil
	}

	var cert tls.Certificate
	if defaultConfig.isCertificateFile {
		cert, err = tls.LoadX509KeyPair(defaultConfig.Certificate, defaultConfig.CertificateKey)
	} else {
		cert, err = tls.X509KeyPair([]byte(defaultConfig.Certificate), []byte(defaultConfig.CertificateKey))
	}

	if err != nil {
		return nil, err
	}

	gatewayParts := strings.Split(defaultConfig.feedback_gateway, ":")
	cachedFeedbackTlsConfig = &tls.Config{
		Certificates: []tls.Certificate{cert},
		ServerName:   gatewayParts[0],
	}

	return cachedFeedbackTlsConfig, nil
}
