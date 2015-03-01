# Golang APNs package#
###Apple Push notification package###

This package is made with the ideea to be able to send a lot of messages
to Apple APNs very very fast atomaticaly scaling with your sending needs

###Package features###
- multiple managed connections to Aple APNs
- non blocking waiting for message confirmation
- try's to ensure the delivery of the message and to give little false positive errors
- push service reporting running each 5 min on info level (each counter is reset after print)
    APNs Report - ActivePool: (current size of the active services), PoolSize: (the current pool size), Queue: (instant queue size), Sent: (total sent), ReSent: (total resent), Confirmed: (success confirmed messages),ConfirmedFailed: (total failed confirmed), ConfirmTimeAvg: (confirm duration avg) SendingTime: (sending enter in push notification until exit avg time)

###How this package works###
- when you post a new message this is going posted to a queue
- the message is going to be take by a managed connection and sent to Apple
- after this the message will be moved to a sent queue where he is
   waiting his status as the status is explained after
- each message will be added to the confirmed queue, where you will receive the status of the message
- make sure you consume the messages from confirmation queue because this can block sending

######A message status will be set like this######
- if a messages was sent more then "SuccessTimeout" ago 			    - OK
- if status 0 (this i think isn't going to happen as apple says)     - OK
- if status 1 it's a proccessing error, we retry                     - FAIL (Temporary, Reconnect)
- if status 2 to 8 it's device token or payload error                - FAIL (Permanent)
- if status 10 the payload was sent                                  - OK (Reconnect)
- if status 255 it's unknown error                                   - FAIL (Temporary, Reconnect)
- if status number unknown                                           - FAIL (Temporary, Reconnect)
- if read/write socket channel closed								- FAIL (Permanent, Reconnect)
    - we make it as permanent on connection close because we don't know the status and this can close all the connections from the pool

######Notes:######
 - Permanent - means that the error it's because of the payload/device token so will not be retried
 - Temporary - means that will fail only after "MaxMessageRetry", until this it will be requeued
 - Reconnect - means that the socket used for that message will be closed and a new one will be created
    - reconnect has a protection not to connect faster then "ReconnectDistanceTime" seconds
    - all that are not confirmed in the queue will be mark as failed and temporary error

###Example###

 certificate/certificateKey can be path to file or the inline certificate

Configuration:

        SuccessTimeout        time.Duration //default 500ms
		MaxMessageRetry       uint 			//default 5
	    MessageQueueSize      uint          //default 1000
	    
        NotficationPriority    uint8         //default 10 (will downgrade to 5 if no sound/alert/badge set)
        NotificationExpiration time.Duration //default 24h because this is can be 0 you must set a value to field or to use the default one set -1 

		IsProduction bool //default false

		Certificate    string //no default value
		CertificateKey string //no default value

		MinPoolSize uint                    //default 1
		MaxPoolSize uint                    //default 2
		UpgradePool uint                    //default 200
		DowngradePool uint                  //default 50 (max val for downgrade is 1/2 from upgrade)
		ReconnectDistanceTime time.Duration //default 10s
		//Connection pool will upgrade if there are more then UpgradePool messages in the queue to be sent new connection it will be added each 10 seconds until the queue will be < UpgradePool or MaxPoolSize will be reachedThe downgrade will remove a connection if 300 seconds the queue will be < DowngradePool or the MinPoolSize will be reached (we wait 300 so apple will not consider us as dos attack)

        ReportingInterval time.Duration //default 5min - the interval between to reports

        FeedbackReadTimeout time.Duration //default 20s - how long to wait for messages on a connection


init returns error if certificates not set or they are wrong, 
you can call multiple time, only if err != nil, will panic if you try to init after a success
    
    cfg := &apns.Config{
        MaxPoolSize: 10,
        SuccessTimeout: time.Miliseccond * 300,
        NotificationExpiration: time.Duration(-1),
    } //this config will overwrite the default ones
    if err:= apns.Init(cfg); err != nil {
    	  //do something
    }
    //see more on payload or you can use any object that conforms to PayloadInterface
    payload := apns.NewPayload()
    payload.Alert = "APNs Testing"

    pushNotification := apns.NewPushNotification()
    //device token is hex string (string len 64)
    pushNotification.DeviceToken = "980f7123456788f1cf31a3fc1eb4d3ec8054b36b4ed158f2c65784925fc0000"
    pushNotification.AddPayload(payload)

    apns.Send(pushNotification)
    pushNotificationStatus := <-apns.Confirmation()

###Feedback Service###
- we can consume the messages  by channel
	     err:= apns.Feedback()

- feedback service and is returning an object with 3 methods Channel, Interval and Stop, on Channel you will recevie all the feedback messages
- Interval check at each interval for new messages
- Interval is default 0, this will make it do only one request then will stop, if interval > 0 then at each interval will check for new message (the timer is started when you request the channel)
- after Stop make sure you read until channel close so you get all the messages
    
###Logging:###
- you can use 2 type's of loggers
````	
apns.SetGoLogger(logger)// if you use go standard logger
apns.SetLogger(logger) //for this you need a logger with Debug/Info/Warning/Error methods as interface apns.Logger
````
### Docs ###

#####Note:#####
 this library conforms with apple specifications
 - for communications
    https://developer.apple.com/library/ios/documentation/NetworkingInternet/Conceptual/RemoteNotificationsPG/Chapters/CommunicatingWIthAPS.html
 - for payload
    https://developer.apple.com/library/ios/documentation/NetworkingInternet/Conceptual/RemoteNotificationsPG/Chapters/ApplePushService.html#//apple_ref/doc/uid/TP40008194-CH100-SW1

###TO DO:###
- unit tests
- time between message resends
- improve feedback service
- examples
