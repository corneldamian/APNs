package apns

import (
	"container/list"
	"crypto/tls"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"strings"
	"sync"
	"time"
)

func newPushService() (*pushService, error) {
	ps := &pushService{}
	if err := ps.createConnection(); err != nil {
		return nil, err
	}

	ps.waitClose = &sync.WaitGroup{}
	ps.waitClose.Add(3)

	ps.sentQueue = make(chan *ApnsMessage, 1000)
	ps.confirmedQueue = make(chan *apnsReceiveMessage, 1000)
	ps.doSenderClose = make(chan bool, 2)

	go ps.monitorReply()
	go ps.monitorMessages()
	go ps.messageSender()

	return ps, nil
}

type pushService struct {
	conn      *tls.Conn
	lastError error

	sentQueue      chan *ApnsMessage
	confirmedQueue chan *apnsReceiveMessage

	doSenderClose chan bool
	waitClose     *sync.WaitGroup
}

func (ps *pushService) createConnection() error {
	conf, err := getTlsConfig()
	if err != nil {
		return err
	}

	conn, err := tls.Dial("tcp", defaultConfig.gateway, conf)
	if err != nil {
		return err
	}

	ps.conn = conn
	return nil
}

func (ps *pushService) destroy() {
	logdebug("Destroy push service %p, waiting to finish", ps)

	ps.doSenderClose <- true //this will close the sender
	ps.conn.Close()          //this will close the socket and force the read to exit
	//(will remove the last message if not confirmed ??? it's ok ???)
	//when sender/reader are closed the sending monitor will close

	ps.waitClose.Wait()
	logdebug("Destroy push service %p finish", ps)
}

func (ps *pushService) messageSender() {
	defer func() {
		ps.waitClose.Done()
		logdebug("Message sender stopped %p", ps)
	}()

	logdebug("Message sender started %p", ps)

	for {
		select {
		case <-ps.doSenderClose:
			close(ps.sentQueue)
			return
		case messages := <-mainSendingMessage:
			logdebug("Sending new message: %d to %s", messages.id, messages.DeviceToken)
			data, err := createBinaryNotification(messages, messages.id, messages.DeviceToken)
			if err != nil {
				messages.SetStatus(err, true)
				break
			}

			_, err = ps.conn.Write(data)
			if err != nil {
				ps.lastError = nil
				pushPool.releasedServices <- ps
				close(ps.sentQueue)
				return
			}
			logdebug("Message id: %d sent", messages.id)
			ps.sentQueue <- messages
			pushPool.reportQueue <- &notifReport{sent: 1}
		}
	}
}

func (ps *pushService) monitorReply() {
	defer func() {
		ps.waitClose.Done()
		logdebug("Message reply stopped %p", ps)
	}()

	logdebug("Message reply started %p", ps)

	readb := [6]byte{}

	for {
		n, err := ps.conn.Read(readb[:])

		if err != nil {
			logerr("APNS read channel is closed %s", err)
			//when the read socket channel is closed the last payload i think isn't sent ok
			//i don't know the last message id so we consider id 0 as last message and don't use this socket
			ps.lastError = err
			ps.confirmedQueue <- &apnsReceiveMessage{lastMessage: true}
			ps.doSenderClose <- true
			close(ps.confirmedQueue)
			break
		}

		if n == 6 {
			// if status 0 (this i think isn't going to happen as apple says)
			// if status 2 to 8 it's device token or payload error, don't try to resend
			// if status 1 it's a proccessing error, we should retry sending the payload
			// if status 10 the payload was sent but we must not send anything else on this socket (is shutdown)
			// if status 255 it's unknown error (let's retry to send the payload)  - close socket
			// if status number unknown ???? (ok, let's retry to send the payload) - close socket

			status := readb[1]
			id := binary.BigEndian.Uint32(readb[2:])

			ps.confirmedQueue <- &apnsReceiveMessage{status: status, id: id}
			logdebug("Received confirmation for id: %d with status: %d", id, status)
			if status != 0 {
				ps.lastError = fmt.Errorf("APNs server reply with status %d (%s), closing", status, statusToError(status))
				ps.doSenderClose <- true
				close(ps.confirmedQueue)
				break
			}
		} else {
			//unknow data sent from apple
			// let's close the socket and mark all the messagess with error
			logerr("Unknow apple message (%s) socket will be closed", hex.EncodeToString(readb[:n]))
			//we consinder that max uint32 means to delete all the messages from the queue
			ps.lastError = fmt.Errorf("Unknow apple message (%s) socket will be closed", hex.EncodeToString(readb[:n]))
			ps.confirmedQueue <- &apnsReceiveMessage{unknownMessage: true}
			ps.doSenderClose <- true
			close(ps.confirmedQueue)
			break
		}
	}
}

func (ps *pushService) monitorMessages() {
	defer func() {
		ps.waitClose.Done()
		logdebug("Message monitor stopped %p", ps)
	}()

	logdebug("Message monitor started %p", ps)

	check := time.Tick(time.Millisecond * 50)
	messageList := newMessageQueue()

	readerStopped := false
	writerStopped := false

	//make sure that we don't have messages in the list
	//what we have on exit we mark as error
	defer func() {
		messageList.SetAllFailed(fmt.Errorf("Message in the remained queue on close"))
		logdebug("Push service is stopping, all the messages from the queue will be reposted")

		if ps.lastError != nil { //anounnce the pool master that we had an error
			pushPool.releasedServices <- ps
		}
	}()

	for {
		select {
		case <-check: // check if we have messages older then SuccessTimeout so we can mark them
			// logdebug("Check to mark message too old as success: %p", ps)
			messageList.SetSuccessOlder(defaultConfig.SuccessTimeout)

		case message, is_open := <-ps.sentQueue:
			if !is_open {
				//writer has exited we return only if the reader is stopped too
				//we are going to clean the message list on exit
				logdebug("Sent queue channel closed for %p", ps)
				writerStopped = true
				if readerStopped {
					logdebug("Confirm channel closed too for: %p", ps)
					return
				}
				ps.sentQueue = nil
				break
			}
			logdebug("New message %d in the confirmation queue %p", message.id, ps)
			messageList.PushBack(message)

		case finishMessage, is_open := <-ps.confirmedQueue:
			if !is_open {
				//reader has exited we return only if the writer is stopped too
				//we are going to clean the message list on exit
				logdebug("Confirm queue channel closed for %p", ps)
				readerStopped = true
				if writerStopped {
					logdebug("Sent channel closed too for: %p", ps)
					return
				}
				ps.confirmedQueue = nil
				break
			}

			//mark all excluding last one as success, we are going to close
			if finishMessage.lastMessage {
				messageList.SetLastFailed(fmt.Errorf("Unknow error after last message sent"))
				logdebug("Going to remove last message from queue with error: %p", ps)
				break
			}

			//mark all as failed
			if finishMessage.unknownMessage {
				messageList.SetAllFailed(fmt.Errorf("Unknown error response from apple, all failed"))
				logdebug("All messages are marked as error :%p", ps)
				break
			}

			//mark all from front to the id as success
			if finishMessage.status == 0 {
				messageList.SetSuccess(finishMessage.id)
				logdebug("Mark message %d as success: %p", finishMessage.id, ps)
				break
			}

			//mark all until this one as success and this one mark it as temporar error
			if finishMessage.status == 1 || finishMessage.status == 255 {
				messageList.SetFailed(finishMessage.id, statusToError(finishMessage.status), false)
				logdebug("Mark message %d with temporar error on status: %d %p", finishMessage.id, finishMessage.status, ps)
				break
			} //success until excluding id, id failed

			//mark all until this one as success and this one mark as permanent error
			if finishMessage.status >= 2 && finishMessage.status <= 8 {
				messageList.SetFailed(finishMessage.id, statusToError(finishMessage.status), true)
				logdebug("Mark message %d with permanend error on status: %d %p", finishMessage.id, finishMessage.status, ps)
				break
			}

			//mark all including this one as success
			if finishMessage.status == 10 {
				messageList.SetSuccess(finishMessage.id)
				messageList.SetAllFailed(fmt.Errorf("Apple it's in mantainance mode, socket shutdown"))
				logdebug("Mark message %d with success and the rest with temp error %p", finishMessage.id, ps)
				break
			} //success until including id

			//the rest of status codes we don't know, we consider that it's another problem
			//from apple so mark the message as temporar error and the front as success
			messageList.SetAllFailed(statusToError(finishMessage.status))
			logdebug("Mark all messages with error: %s - %p", statusToError(finishMessage.status), ps)
		}
	}
}

type apnsReceiveMessage struct {
	unknownMessage bool
	lastMessage    bool
	status         uint8
	id             uint32
}

type ApnsMessage struct {
	PayloadInterface

	DeviceToken string
	Error       error

	id           uint32
	lastSentTime time.Time
	receivedTime time.Time
	sentTimes    uint
}

func (a *ApnsMessage) SetStatus(err error, permanentError bool) {
	a.Error = err

	shouldConfirm := false
	if err == nil || permanentError {
		shouldConfirm = true
	}

	a.sentTimes++
	if a.sentTimes >= defaultConfig.MaxMessageRetry {
		shouldConfirm = true
	}

	if shouldConfirm {
		nr := &notifReport{confirmTime: time.Since(a.lastSentTime), sendingTime: time.Since(a.receivedTime)}

		if a.Error != nil {
			nr.failed = 1
		} else {
			nr.success = 1
		}

		pushPool.reportQueue <- nr
		confirmMessage(a)
	} else {
		pushPool.reportQueue <- &notifReport{failed: 1, resent: 1, confirmTime: time.Since(a.lastSentTime)}
		resendMessage(a)
	}
}

func newMessageQueue() *apnsMessageQueue {
	return &apnsMessageQueue{list.New()}
}

type apnsMessageQueue struct {
	mList *list.List
}

func (amq *apnsMessageQueue) PushBack(m *ApnsMessage) {
	m.lastSentTime = time.Now()
	amq.mList.PushBack(m)
}

func (amq *apnsMessageQueue) SetSuccessOlder(older time.Duration) {
	var next *list.Element
	for e := amq.mList.Front(); e != nil; e = next {
		next = e.Next()

		message := e.Value.(*ApnsMessage)

		if time.Since(message.lastSentTime) > older {
			logdebug("Mark message %d as success after timeout", message.id)
			message.SetStatus(nil, false)
			amq.mList.Remove(e)
		}
	}
}

func (amq *apnsMessageQueue) SetSuccess(id uint32) {
	var next *list.Element

	for e := amq.mList.Front(); e != nil; e = next {
		next = e.Next()

		message := e.Value.(*ApnsMessage)

		message.SetStatus(nil, false)
		amq.mList.Remove(e)

		if message.id == id {
			return
		}
	}

	logwarn("I didn't found message %d to set it as success", id)
}

func (amq *apnsMessageQueue) SetFailed(id uint32, err error, permanentError bool) {
	var next *list.Element

	logdebug("Set message %d to status failed permanent: %t (%s)", id, permanentError, err)
	for e := amq.mList.Front(); e != nil; e = next {
		next = e.Next()

		message := e.Value.(*ApnsMessage)

		amq.mList.Remove(e)

		if message.id == id {
			message.SetStatus(err, permanentError)
			return
		} else {
			message.SetStatus(nil, false)
		}
	}

	logwarn("I didn't found message %d to set it as failed, permanent: %t (%s)", id, permanentError, err)
}

func (amq *apnsMessageQueue) SetLastFailed(err error) {
	last := amq.mList.Back()
	if last == nil {
		logdebug("No message found in the queue when i wanted to delete the last one")
		return
	}
	message := last.Value.(*ApnsMessage)

	logdebug("Set LAST message %d to status failed", message.id)

	amq.SetFailed(message.id, err, true)
}

func (amq *apnsMessageQueue) SetAllFailed(err error) {
	logdebug("Set all messages to failed: %s", err)

	var next *list.Element
	for e := amq.mList.Front(); e != nil; e = next {
		next = e.Next()

		message := e.Value.(*ApnsMessage)

		message.SetStatus(err, false)
		amq.mList.Remove(e)
	}
}

type PayloadInterface interface {
	ToJson() ([]byte, error)
	Config() (expiration uint32, priority uint8)
}

func statusToError(status uint8) error {
	e, ok := appleErrorMap[status]
	if ok {
		return fmt.Errorf(e)
	}

	return fmt.Errorf("Unknown apple status error, check doc, maybe it's new: %d", status)
}

var appleErrorMap = map[uint8]string{
	0:   "No errors encountered",
	1:   "Processing Errors",
	2:   "Missing device token",
	3:   "Missing topic",
	4:   "Missing payload",
	5:   "Invalid token size",
	6:   "Invalid topic size",
	7:   "Invalid payload size",
	8:   "Invalid token",
	10:  "Shutdown",
	255: "None (unknown)",
}

var cachedTlsConfig *tls.Config

func getTlsConfig() (cfg *tls.Config, err error) {
	if cachedTlsConfig != nil {
		return cachedTlsConfig, nil
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

	gatewayParts := strings.Split(defaultConfig.gateway, ":")
	cachedTlsConfig = &tls.Config{
		Certificates: []tls.Certificate{cert},
		ServerName:   gatewayParts[0],
	}

	return cachedTlsConfig, nil
}
