package mq

import (
	"encoding/json"
	"github.com/spf13/viper"
	"github.com/tidwall/gjson"
	"strings"
)

const TypeMessage = "message"
const TypeWaitingUser = "waiting-user"
const TypeAdmin = "admin"
const TypeOtherLogin = "other-login"
const TypeTransfer = "admin-transfer"
const TypeWaitingUserCount = "waiting-user-count"
const TypeUpdateSetting = "update-admin-setting"

type MessageQueue interface {
	// Publish 消息
	Publish(channel string, p *Payload) error
	// Subscribe 消息
	Subscribe(channel string) SubScribeChannel
}

type SubScribeChannel interface {
	// ReceiveMessage 接收消息
	ReceiveMessage() gjson.Result
	Close()
}


type Payload struct {
	Types string `json:"types"`
	Data interface{} `json:"data"`
}

func (payload *Payload) MarshalBinary() ([]byte, error) {
	return json.Marshal(payload)
}

func NewMessagePayload(mid uint64) *Payload  {
	return &Payload{
		Types: TypeMessage,
		Data:  mid,
	}
}

var mq MessageQueue

func init()  {
	switch strings.ToLower(viper.GetString("App.Mq")) {
	case "rabbitmq":
		mq = newRabbitMq()
	case "redis":
		mq = newRedisMq()
	default:
		mq = newRedisMq()
	}
}

func Mq() MessageQueue {
	return mq
}