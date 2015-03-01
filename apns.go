package apns

import (
	"fmt"
	"log"
	"strings"
	"sync/atomic"
	"time"
)

const (
	SANDBOX_GATEWAY    = "gateway.sandbox.push.apple.com:2195"
	PRODUCTION_GATEWAY = "gateway.push.apple.com:2195"

	SANDBOX_FEEDBACK_GATEWAY    = "feedback.push.apple.com:2196"
	PRODUCTION_FEEDBACK_GATEWAY = "feedback.sandbox.push.apple.com:2196"
)

var (
	inited bool
)

func Init(cfg Config) error {
	if inited {
		log.Panic("APNS libray already inited")
	}

	cfg.isCertificateFile = !strings.Contains(cfg.Certificate, "BEGIN CERTIFICATE")

	if cfg.isCertificateFile && strings.Contains(cfg.CertificateKey, "BEGIN PRIVATE KEY") {
		logerr("APNS: both certificate and certificateKey must have same type (file or string)")
		return fmt.Errorf("APNS: both certificate and certificateKey must have same type (file or string)")
	}

	setNewConfig(&cfg)

	if defaultConfig.MinPoolSize > defaultConfig.MaxPoolSize {
		logerr("APNS: min pool size > max pool size (%d > %d)", defaultConfig.MinPoolSize, defaultConfig.MaxPoolSize)
		return fmt.Errorf("APNS: min pool size > max pool size (%d > %d)", defaultConfig.MinPoolSize, defaultConfig.MaxPoolSize)
	}

	if defaultConfig.DowngradePool*2 > defaultConfig.UpgradePool {
		logerr("APNS: downgrade pool must be < (upgrade pool / 2) (%d > %d/2)", defaultConfig.MinPoolSize, defaultConfig.MaxPoolSize)
		return fmt.Errorf("APNS: downgrade pool must be < (upgrade pool / 2) (%d > %d/2)", defaultConfig.MinPoolSize, defaultConfig.MaxPoolSize)
	}

	certificateType := "embeded string"
	if defaultConfig.isCertificateFile {
		certificateType = "file"
	}

	enviroment := "SANDBOX"
	if defaultConfig.IsProduction {
		enviroment = "PRODUCTION"
	}

	loginfo("APNS will init with %s enviroment and certificate from %s", enviroment, certificateType)

	mainSendingMessage = make(chan *ApnsMessage, defaultConfig.MessageQueueSize)
	mainConfirmationQueue = make(chan *ApnsMessage, defaultConfig.MessageQueueSize)

	initPushService()
	inited = true

	return nil
}

//send a apns message, 0 and max uint32 id's are reserved, don't use them
//the status of the payload must be read from Confirmation channel
//you must read the confirm or after MessageQueueSize messages will block start blocking
//sent is safe to be called from multiple go rutines
func Send(payload PayloadInterface, deviceToken string) (messageIdentification uint32) {
	if !inited {
		panic("Send called and apns lib isn't inited")
	}

	id := atomic.AddUint32(&messageId, 1)
	message := &ApnsMessage{PayloadInterface: payload, DeviceToken: deviceToken, id: id}
	message.receivedTime = time.Now()

	sendMessage(message)

	return id
}

func sendMessage(message *ApnsMessage) {
	atomic.AddInt64(&messagesInQueue, 1)

	mainSendingMessage <- message
}

func resendMessage(message *ApnsMessage) {
	atomic.AddInt64(&messagesInQueue, -1)

	sendMessage(message)
}

//you will receive the message sent status on this channel
//on error the SetError will be called on the payload and with that you can check the why and if it has failed
//you must read the confirm or after MessageQueueSize messages will block start blocking
//the error will be the last error from the retry
//confirm is safe to be called from multiple go rutines
func Confirm() <-chan *ApnsMessage {
	if !inited {
		panic("Confirmation called and apns lib isn't inited")
	}
	return mainConfirmationQueue
}

func confirmMessage(message *ApnsMessage) {
	atomic.AddInt64(&messagesInQueue, -1)

	mainConfirmationQueue <- message
}

var (
	mainSendingMessage    chan *ApnsMessage
	mainConfirmationQueue chan *ApnsMessage

	messagesInQueue int64  = 0
	messageId       uint32 = 0
)

func setNewConfig(cfg *Config) {
	defaultConfig.Certificate = cfg.Certificate
	defaultConfig.CertificateKey = cfg.CertificateKey
	defaultConfig.isCertificateFile = cfg.isCertificateFile

	defaultConfig.IsProduction = cfg.IsProduction
	defaultConfig.gateway = PRODUCTION_GATEWAY
	defaultConfig.feedback_gateway = PRODUCTION_FEEDBACK_GATEWAY
	if !defaultConfig.IsProduction {
		defaultConfig.gateway = SANDBOX_GATEWAY
		defaultConfig.feedback_gateway = SANDBOX_FEEDBACK_GATEWAY
	}

	if cfg.SuccessTimeout > 0 {
		defaultConfig.SuccessTimeout = cfg.SuccessTimeout
	}
	if cfg.MaxMessageRetry > 0 {
		defaultConfig.MaxMessageRetry = cfg.MaxMessageRetry
	}
	if cfg.MessageQueueSize > 0 {
		defaultConfig.MessageQueueSize = cfg.MessageQueueSize
	}
	if cfg.NotificationPriority > 0 {
		defaultConfig.NotificationPriority = cfg.NotificationPriority
	}
	if cfg.NotificationExpiration >= 0 {
		defaultConfig.NotificationExpiration = cfg.NotificationExpiration
	}

	if cfg.ReconnectDistanceTime > 0 {
		defaultConfig.ReconnectDistanceTime = cfg.ReconnectDistanceTime
	}
	if cfg.UpgradePool > 0 {
		defaultConfig.UpgradePool = cfg.UpgradePool
	}
	if cfg.DowngradePool > 0 {
		defaultConfig.DowngradePool = cfg.DowngradePool
	}
	if cfg.MinPoolSize > 0 {
		defaultConfig.MinPoolSize = cfg.MinPoolSize
	}
	if cfg.MaxPoolSize > 0 {
		defaultConfig.MaxPoolSize = cfg.MaxPoolSize
	}

	if cfg.ReportingInterval > 0 {
		defaultConfig.ReportingInterval = cfg.ReportingInterval
	}

	if cfg.FeedbackReadTimeout > 0 {
		defaultConfig.FeedbackReadTimeout = cfg.FeedbackReadTimeout
	}
}

var defaultConfig = &Config{
	SuccessTimeout:         time.Millisecond * 500,
	MaxMessageRetry:        5,
	MessageQueueSize:       1000,
	NotificationPriority:   10,
	NotificationExpiration: time.Hour * 24,

	IsProduction: false,

	ReconnectDistanceTime: time.Second * 10,
	MinPoolSize:           1,
	MaxPoolSize:           2,
	UpgradePool:           200,
	DowngradePool:         50,

	ReportingInterval: time.Minute * 5,

	FeedbackReadTimeout: time.Second * 20,
}

type Config struct {
	SuccessTimeout   time.Duration //the duration after that a message without response will be considered success
	MaxMessageRetry  uint          //max times to retry and send a message before we reported as failed
	MessageQueueSize uint          //max number of messages that are going to be in the send/confirm and feedback channel
	//note that the channel will block if is going to be full
	NotificationPriority   uint8         // the default priority is 10 (if alert/sound/badge not set, will be forced to 5)
	NotificationExpiration time.Duration // the default duration after wich apple will expire the notif

	IsProduction     bool //should i use production or sandbox enviroment ?
	gateway          string
	feedback_gateway string

	Certificate       string //inline certificate or file path (note certificate and the key must be the same file or inline)
	CertificateKey    string //inline certificate key or file path
	isCertificateFile bool

	ReconnectDistanceTime time.Duration //the min distance to reconnect a failed connection
	MinPoolSize           uint          //min connection pool size
	MaxPoolSize           uint          //max connection pool size
	UpgradePool           uint          //the number of messages in the queue (for 10s) after wich the pool will be upgraded
	DowngradePool         uint          //the number of messages in the queue (for 5 min) after wich the poll will be downgraded

	ReportingInterval time.Duration //the interval between to reports

	FeedbackReadTimeout time.Duration //how long to wait for messages on a connection
}

var (
	loggerself Logger
	loggergo   *log.Logger
)

func SetLogger(logger Logger) {
	loggerself = logger
}

func SetGoLogger(logger *log.Logger) {
	loggergo = logger
}

func logdebug(format string, args ...interface{}) {
	if loggerself != nil {
		loggerself.Debug(format, args...)
	} else if loggergo != nil {
		loggergo.Printf(format, args...)
	}
}

func loginfo(format string, args ...interface{}) {
	if loggerself != nil {
		loggerself.Info(format, args...)
	} else if loggergo != nil {
		loggergo.Printf(format, args...)
	}
}

func logwarn(format string, args ...interface{}) {
	if loggerself != nil {
		loggerself.Warning(format, args...)
	} else if loggergo != nil {
		loggergo.Printf(format, args...)
	}
}

func logerr(format string, args ...interface{}) {
	if loggerself != nil {
		loggerself.Error(format, args...)
	} else if loggergo != nil {
		loggergo.Printf(format, args...)
	}
}

type Logger interface {
	Debug(format string, args ...interface{})
	Info(format string, args ...interface{})
	Warning(format string, args ...interface{})
	Error(format string, args ...interface{})
}
