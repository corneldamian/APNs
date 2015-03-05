package apns

import (
	"bytes"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"time"
)

const (
	pushCommandVersion       = 2    //push notification command version3
	MaxNotificationSizeBytes = 2048 //max notification data size (2K)
)

func NewPushNotification() *PushNotification {
	return &PushNotification{payload: make(map[string]interface{}), priority: defaultConfig.NotificationPriority}
}

type PushNotification struct {
	payload    map[string]interface{}
	expiration uint32
	priority   uint8
}

func (pn *PushNotification) ToJson() (data []byte, err error) {
	data, err = json.Marshal(pn.payload)
	return
}

func (pn *PushNotification) Config() (expiration uint32, priority uint8) {
	if n, ok := pn.payload["aps"]; ok {
		nn := n.(*notification)
		if nn.Alert == nil && nn.Sound == nil && nn.Badge == nil {
			pn.SetPriority(5)
		}
	}

	return pn.expiration, pn.priority
}

func (pn *PushNotification) SetExpire(expire time.Duration) {
	pn.expiration = uint32(time.Now().Add(expire).Unix())
}

func (pn *PushNotification) SetPriority(priority uint8) {
	pn.priority = priority
}

func (pn *PushNotification) SetNotification(n *notification) {
	pn.payload["aps"] = n
}

func (pn *PushNotification) SetCustom(key string, value interface{}) {
	pn.payload[key] = value
}

func NewNotification() *notification {
	return &notification{}
}

type notification struct {
	Alert            interface{} `json:"alert,omitempty"`
	Badge            *int        `json:"badge,omitempty"`
	Sound            *string     `json:"sound,omitempty"`
	ContentAvailable *int        `json:"content-available,omitempty"`
}

func (n *notification) SetAlert(alert interface{}) {
	n.Alert = alert
}

func (n *notification) SetBadge(number int) {
	n.Badge = &number
}

func (n *notification) SetSound(sound string) {
	n.Sound = &sound
}

func (n *notification) SetContentAvailable(number int) {
	n.ContentAvailable = &number
}

func NewAlertDictionary() *alertDictionary {
	return &alertDictionary{}
}

type alertDictionary struct {
	Title        *string  `json:"title,omitempty"`
	Body         *string  `json:"body,omitempty"`
	TitleLocKey  *string  `json:"title-loc-key,omitempty"`
	TitleLocArgs []string `json:"title-loc-args,omitempty"`
	ActionLocKey *string  `json:"action-loc-key,omitempty"`
	LocKey       *string  `json:"loc-key,omitempty"`
	LocArgs      []string `json:"loc-args,omitempty"`
	LaunchImage  *string  `json:"launch-image,omitempty"`
}

func (ad *alertDictionary) SetTitle(title string) {
	ad.Title = &title
}

func (ad *alertDictionary) SetBody(body string) {
	ad.Body = &body
}

func (ad *alertDictionary) SetTitleLoc(key string, args []string) {
	ad.TitleLocKey = &key
	ad.TitleLocArgs = args
}

func (ad *alertDictionary) SetActionLocKey(key string) {
	ad.ActionLocKey = &key
}

func (ad *alertDictionary) SetLoc(key string, args []string) {
	ad.LocKey = &key
	ad.LocArgs = args
}

func (ad *alertDictionary) SetLaunchImage(image string) {
	ad.LaunchImage = &image
}

const (
	deviceTokenItemId            uint8 = 1
	payloadItemId                uint8 = 2
	notificationIdentifierItemId uint8 = 3
	expirationDateItemId         uint8 = 4
	priorityItemId               uint8 = 5

	deviceTokenLen            uint16 = 32
	payloadIdentifierLen      uint16 = 4
	expirationDateLen         uint16 = 4
	priorityLen               uint16 = 1
	notificationIdentifierLen uint16 = 4
)

func createBinaryNotification(pn PayloadInterface, identifier uint32, deviceToken string) ([]byte, error) {
	token, err := hex.DecodeString(deviceToken)
	if err != nil {
		return nil, err
	}

	if uint16(len(token)) != deviceTokenLen {
		return nil, fmt.Errorf("Device token len is %d must be %d (after string to hex, so string is double)", len(token), deviceTokenLen)
	}

	payload, err := pn.ToJson()
	if err != nil {
		return nil, err
	}
	logdebug("JSON: %s", string(payload))
	if len(payload) > MaxNotificationSizeBytes {
		return nil, fmt.Errorf("Max notification size is: %d, you try to send: %d", MaxNotificationSizeBytes, len(payload))
	}

	expiration, priority := pn.Config()

	frameDataBuf := new(bytes.Buffer)
	binary.Write(frameDataBuf, binary.BigEndian, deviceTokenItemId)
	binary.Write(frameDataBuf, binary.BigEndian, deviceTokenLen)
	binary.Write(frameDataBuf, binary.BigEndian, token)
	binary.Write(frameDataBuf, binary.BigEndian, payloadItemId)
	binary.Write(frameDataBuf, binary.BigEndian, uint16(len(payload)))
	binary.Write(frameDataBuf, binary.BigEndian, payload)
	binary.Write(frameDataBuf, binary.BigEndian, notificationIdentifierItemId)
	binary.Write(frameDataBuf, binary.BigEndian, notificationIdentifierLen)
	binary.Write(frameDataBuf, binary.BigEndian, identifier)
	binary.Write(frameDataBuf, binary.BigEndian, expirationDateItemId)
	binary.Write(frameDataBuf, binary.BigEndian, expirationDateLen)
	binary.Write(frameDataBuf, binary.BigEndian, expiration)
	binary.Write(frameDataBuf, binary.BigEndian, priorityItemId)
	binary.Write(frameDataBuf, binary.BigEndian, priorityLen)
	binary.Write(frameDataBuf, binary.BigEndian, priority)

	buffer := bytes.NewBuffer([]byte{})
	binary.Write(buffer, binary.BigEndian, uint8(pushCommandVersion))
	binary.Write(buffer, binary.BigEndian, uint32(frameDataBuf.Len()))
	binary.Write(buffer, binary.BigEndian, frameDataBuf.Bytes())

	return buffer.Bytes(), nil
}
