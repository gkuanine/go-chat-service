package hub

import (
	"sort"
	"ws/action"
	"ws/db"
	"ws/models"
	"ws/resources"
)

type serviceHub struct {
	BaseHub
}

func (hub *serviceHub) Setup() {
	hub.Register(UserLogin, func(i ...interface{}) {
		hub.BroadcastServiceUser()
		hub.BroadcastWaitingUser()
	})
	hub.Register(UserLogout, func(i ...interface{}) {
		hub.BroadcastServiceUser()
	})
}
func (hub *serviceHub) BroadcastWaitingUser() {
	var messages []*models.Message
	db.Db.Preload("User").
		Order("received_at desc").
		Where("service_id = ?", 0).Find(&messages)
	waitingUserMap := make(map[int64]*resources.WaitingUser)
	for _, message := range messages {
		if message.User.ID != 0{
			if wU, exist := waitingUserMap[message.User.ID]; !exist {
				waitingUserMap[message.User.ID] =  &resources.WaitingUser{
					Username:     message.User.Username,
					Avatar:       "",
					Id:           message.User.ID,
					LastMessage:  message.Content,
					LastTime:     message.ReceivedAT,
					MessageCount: 1,
					Description:  "",
				}
			} else {
				wU.MessageCount += 1
			}
		}
	}
	waitingUserSlice := make([]*resources.WaitingUser, 0)
	for _, user := range waitingUserMap {
		waitingUserSlice = append(waitingUserSlice, user)
	}
	sort.Slice(waitingUserSlice, func(i, j int) bool {
		return waitingUserSlice[i].LastTime > waitingUserSlice[j].LastTime
	})
	conns := hub.GetAllConn()
	hub.SendAction(action.NewWaitingUsers(waitingUserSlice),  conns...)
}
func (hub *serviceHub) BroadcastServiceUser() {
	var serviceUsers []*models.ServiceUser
	db.Db.Find(&serviceUsers)
	conns := hub.GetAllConn()
	data := make([]resources.ChatServiceUser, 0)
	for _, serviceUser := range serviceUsers {
		_, online := hub.GetConn(serviceUser.ID)
		data = append(data, resources.ChatServiceUser{
			Avatar: serviceUser.GetAvatarUrl(),
			Username: serviceUser.Username,
			Online: online,
			Id: serviceUser.ID,
			TodayAcceptCount: serviceUser.GetTodayAcceptCount(),
		})
	}
	hub.SendAction(action.NewServiceUserAction(data), conns...)
}