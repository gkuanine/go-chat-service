package websocket

import (
	"fmt"
	"sort"
	"time"
	"ws/app/chat"
	"ws/app/contract"
	"ws/app/log"
	"ws/app/models"
	"ws/app/mq"
	"ws/app/repositories"
	"ws/app/resource"
	"ws/app/util"

	"github.com/gorilla/websocket"
	uuid "github.com/satori/go.uuid"
	"github.com/spf13/viper"
)

var AdminManager *adminManager

type adminManager struct {
	manager
}

func NewAdminConn(user *models.Admin, conn *websocket.Conn) Conn {
	return &Client{
		conn:        conn,
		closeSignal: make(chan interface{}),
		send:        make(chan *Action, 100),
		manager:     AdminManager,
		User:        user,
		uuid:        uuid.NewV4().String(),
	}
}

func SetupAdmin() {
	AdminManager = &adminManager{
		manager: manager{
			shardCount:   10,
			Channel:      util.GetIPs()[0] + ":" + viper.GetString("Http.Port") + "-admin",
			ConnMessages: make(chan *ConnMessage, 100),
			types:        "admin",
		},
	}
	AdminManager.onRegister = AdminManager.registerHook
	AdminManager.onUnRegister = AdminManager.unregisterHook
	AdminManager.Run()
}

func (m *adminManager) Run() {
	m.manager.Run()
	go m.handleReceiveMessage()
	m.Do(func() {
		go m.handleRemoteMessage()
	}, nil)

}

// DeliveryMessage
// 投递消息
// 查询admin是否在本机上，是则直接投递
// 查询admin当前channel，如果存在则投递到该channel上
// 最后则说明admin不在线，处理离线逻辑
func (m *adminManager) DeliveryMessage(msg *models.Message, remote bool) {
	adminConn, exist := m.GetConn(msg.GetAdmin())
	if exist { // admin在线且在当前服务上
		UserManager.triggerMessageEvent(models.SceneAdminOnline, msg)
		adminConn.Deliver(NewReceiveAction(msg))
		return
	} else if !remote && m.isCluster() {
		adminChannel := m.getUserChannel(msg.AdminId) // 获取用户所在channel
		if adminChannel != "" {
			_ = m.publish(adminChannel, mq.NewMessagePayload(msg.Id))
			return
		}
	}
	m.handleOffline(msg)

}

// 从管道接受消息并处理
func (m *adminManager) handleReceiveMessage() {
	for {
		payload := <-m.ConnMessages
		go m.handleMessage(payload)
	}
}

// 处理离线消息
func (m *adminManager) handleOffline(msg *models.Message) {
	UserManager.triggerMessageEvent(models.SceneAdminOffline, msg)
	admin := repositories.AdminRepo.FirstById(msg.AdminId)
	setting := admin.GetSetting()
	if setting != nil {
		// 发送离线消息
		if setting.OfflineContent != "" {
			offlineMsg := setting.GetOfflineMsg(msg.UserId, msg.SessionId, msg.GroupId)
			offlineMsg.Admin = admin
			repositories.MessageRepo.Save(offlineMsg)
			UserManager.DeliveryMessage(offlineMsg, false)
		}
		// 判断是否自动断开
		lastOnline := setting.LastOnline
		duration := chat.SettingService.GetOfflineDuration(msg.GroupId)
		if (lastOnline.Unix() + duration) < time.Now().Unix() {
			chat.SessionService.Close(msg.SessionId, false, true)
			noticeMessage := admin.GetBreakMessage(msg.UserId, msg.SessionId) // 断开提醒
			noticeMessage.Save()
			UserManager.DeliveryMessage(noticeMessage, false)
		}
	}
}

// 订阅本manager的channel， 处理消息
func (m *adminManager) handleRemoteMessage() {
	subscribe := mq.Mq().Subscribe(m.GetSubscribeChannel())
	defer subscribe.Close()
	for {
		message := subscribe.ReceiveMessage()
		go func() {
			log.Log.Info(message.Get("types"))
			switch message.Get("types").String() {
			case mq.TypeWaitingUser:
				fmt.Println(mq.TypeWaitingUser)
				gid := message.Get("data").Int()
				if gid > 0 {
					m.broadcastWaitingUser(gid)
				}
			case mq.TypeAdmin:
				gid := message.Get("data").Int()
				if gid > 0 {
					m.broadcastAdmins(gid)
				}
			case mq.TypeOtherLogin:
				uid := message.Get("data").Int()
				if uid > 0 {
					user := repositories.AdminRepo.FirstById(uid)
					if user != nil {
						m.NoticeOtherLogin(user)
					}
				}
			case mq.TypeMoreThanOne:
				uid := message.Get("data").Int()
				if uid > 0 {
					user := repositories.AdminRepo.FirstById(uid)
					if user != nil {
						m.noticeMoreThanOne(user)
					}
				}
			case mq.TypeTransfer:
				adminId := message.Get("data").Int()
				if adminId > 0 {
					admin := repositories.AdminRepo.FirstById(adminId)
					if admin != nil {
						m.broadcastUserTransfer(admin)
					}
				}
			case mq.TypeMessage:
				mid := message.Get("data").Int()
				msg := repositories.MessageRepo.FirstById(mid)
				if msg != nil {
					m.DeliveryMessage(msg, true)
				}
			case mq.TypeUpdateSetting:
				id := message.Get("data").Int()
				admin := repositories.AdminRepo.First([]*repositories.Where{{
					Filed: "id = ?",
					Value: id,
				}}, []string{})
				if admin != nil {
					m.updateSetting(admin)
				}
			case mq.TypeUserOnline:
				userId := message.Get("data").Int()
				m.NoticeUserOnline(userId)
			case mq.TypeUserOffline:
				userId := message.Get("data").Int()
				m.NoticeUserOffline(userId)
			}
		}()
	}
}

// 处理消息
func (m *adminManager) handleMessage(payload *ConnMessage) {
	act := payload.Action
	conn := payload.Conn
	switch act.Action {
	// 客服发送消息给用户
	case SendMessageAction:
		msg, err := act.GetMessage()
		if err == nil {
			if msg.UserId > 0 && len(msg.Content) != 0 {
				if !chat.AdminService.IsUserValid(conn.GetUserId(), msg.UserId) {
					conn.Deliver(NewErrorMessage("该用户已失效，无法发送消息"))
					return
				}
				session := repositories.ChatSessionRepo.FirstActiveByUser(msg.UserId, conn.GetUserId())
				if session == nil {
					conn.Deliver(NewErrorMessage("无效的用户"))
					return
				}
				msg.GroupId = conn.GetGroupId()
				msg.AdminId = conn.GetUserId()
				msg.Source = models.SourceAdmin
				msg.ReceivedAT = time.Now().Unix()
				msg.Admin = conn.User.(*models.Admin)
				msg.SessionId = session.Id
				repositories.MessageRepo.Save(msg)
				_ = chat.AdminService.UpdateUser(msg.AdminId, msg.UserId)
				// 服务器回执d
				conn.Deliver(NewReceiptAction(msg))
				UserManager.DeliveryMessage(msg, false)
			}
		}
	}
}

func (m *adminManager) registerHook(conn Conn) {
	m.broadcastUserTransfer(conn.GetUser())
	m.PublishAdmins(conn.GetGroupId())
	m.broadcastWaitingUser(conn.GetGroupId())
}

// conn断开连接后，更新admin的最后在线时间
func (m *adminManager) unregisterHook(conn Conn) {
	u := conn.GetUser()
	admin, ok := u.(*models.Admin)
	if ok {
		setting := admin.GetSetting()
		repositories.AdminRepo.UpdateSetting(setting, "last_online", time.Now())
	}
	m.PublishAdmins(conn.GetGroupId())
}

// PublishWaitingUser 推送待接入用户
func (m *adminManager) PublishWaitingUser(groupId int64) {
	m.Do(func() {
		m.publishToAllChannel(&mq.Payload{
			Types: mq.TypeWaitingUser,
			Data:  groupId,
		})
	}, func() {
		m.broadcastWaitingUser(groupId)
	})
}
func (m *adminManager) PublishUserOffline(user contract.User) {
	m.Do(func() {
		adminId := chat.UserService.GetValidAdmin(user.GetPrimaryKey())
		if adminId > 0 {
			channel := m.getUserChannel(adminId)
			if channel != "" {
				_ = m.publish(channel, &mq.Payload{
					Types: mq.TypeUserOffline,
					Data:  user.GetPrimaryKey(),
				})
			}
		}
	}, func() {
		m.NoticeUserOffline(user.GetPrimaryKey())
	})
}
func (m *adminManager) PublishUserOnline(user contract.User) {
	m.Do(func() {
		adminId := chat.UserService.GetValidAdmin(user.GetPrimaryKey())
		if adminId > 0 {
			channel := m.getUserChannel(adminId)
			if channel != "" {
				_ = m.publish(channel, &mq.Payload{
					Types: mq.TypeUserOnline,
					Data:  user.GetPrimaryKey(),
				})
			}
		}
	}, func() {
		m.NoticeUserOnline(user.GetPrimaryKey())
	})
}

// NoticeUserOffline 通知用户离线
func (m *adminManager) NoticeUserOffline(uid int64) {
	adminId := chat.UserService.GetValidAdmin(uid)
	admin := repositories.AdminRepo.FirstById(adminId)
	if admin != nil {
		conn, exist := m.GetConn(admin)
		if exist {
			m.SendAction(NewUserOffline(uid), conn)
		}
	}
}

// NoticeUserOnline 通知用户上线
func (m *adminManager) NoticeUserOnline(uid int64) {
	adminId := chat.UserService.GetValidAdmin(uid)
	admin := repositories.AdminRepo.FirstById(adminId)
	if admin != nil {
		conn, exist := m.GetConn(admin)
		if exist {
			m.SendAction(NewUserOnline(uid), conn)
		}
	}
}
func (m *adminManager) PublishOtherLogin(user contract.User) {
	m.Do(func() {
		channel := m.getUserChannel(user.GetPrimaryKey())
		if channel != "" {
			_ = m.publish(channel, &mq.Payload{
				Types: mq.TypeOtherLogin,
				Data:  user.GetPrimaryKey(),
			})
		}
	}, func() {
		m.NoticeOtherLogin(user)
	})
}

// NoticeOtherLogin 重复登录
func (m *adminManager) NoticeOtherLogin(admin contract.User) {
	conn, exist := m.GetConn(admin)
	if exist && conn.GetUuid() != m.GetUserUuid(admin) {
		m.SendAction(NewOtherLogin(), conn)
	}
}

// PublishTransfer 推送待转接的用户
func (m *adminManager) PublishTransfer(admin contract.User) {
	m.Do(func() {
		m.publishToAllChannel(&mq.Payload{
			Types: mq.TypeTransfer,
			Data:  admin.GetPrimaryKey(),
		})
	}, func() {
		m.broadcastUserTransfer(admin)
	})
}

// PublishUpdateSetting admin修改设置后通知conn 更新admin的设置信息
func (m *adminManager) PublishUpdateSetting(admin contract.User) {
	m.Do(func() {
		channel := m.getUserChannel(admin.GetPrimaryKey())
		if channel != "" {
			_ = m.publish(channel, &mq.Payload{
				Types: mq.TypeUpdateSetting,
				Data:  admin.GetPrimaryKey(),
			})
		}
	}, func() {
		m.updateSetting(admin)
	})
}

// PublishAdmins 推送在线admin
func (m *adminManager) PublishAdmins(gid int64) {
	m.Do(func() {
		m.publishToAllChannel(&mq.Payload{
			Types: mq.TypeAdmin,
			Data:  gid,
		})
	}, func() {
		m.broadcastAdmins(gid)
	})
}

// 更新设置
func (m *adminManager) updateSetting(admin contract.User) {
	conn, exist := m.GetConn(admin)
	if exist {
		u, ok := conn.GetUser().(*models.Admin)
		if ok {
			u.RefreshSetting()
		}
	}
}

// 广播待接入用户
func (m *adminManager) broadcastWaitingUser(groupId int64) {
	log.Log.Info("广播待接入用户")
	sessions := repositories.ChatSessionRepo.GetWaitHandles()
	userMap := make(map[int64]*models.User)
	waitingUser := make([]*resource.WaitingChatSession, 0, len(sessions))
	for _, session := range sessions {
		userMap[session.UserId] = session.User
		msgs := make([]*resource.SimpleMessage, 0, len(session.Messages))
		for _, m := range session.Messages {
			msgs = append(msgs, &resource.SimpleMessage{
				Type:    m.Type,
				Time:    m.ReceivedAT,
				Content: m.Content,
			})
		}
		waitingUser = append(waitingUser, &resource.WaitingChatSession{
			Username:     session.User.GetUsername(),
			Avatar:       session.User.GetAvatarUrl(),
			UserId:       session.User.GetPrimaryKey(),
			MessageCount: len(session.Messages),
			Description:  "",
			Messages:     msgs,
			LastTime:     session.QueriedAt,
			SessionId:    session.Id,
		})
	}
	sort.Slice(waitingUser, func(i, j int) bool {
		return waitingUser[i].LastTime > waitingUser[j].LastTime
	})
	adminConns := m.GetAllConn(groupId)
	for _, conn := range adminConns {
		adminUserSlice := make([]*resource.WaitingChatSession, 0)
		for _, userJson := range waitingUser {
			u := userMap[userJson.UserId]
			admin := conn.GetUser().(*models.Admin)
			if admin.AccessTo(u) {
				adminUserSlice = append(adminUserSlice, userJson)
			}
		}
		conn.Deliver(NewWaitingUsers(adminUserSlice))
	}
}

// 向admin推送待转接入的用户
func (m *adminManager) broadcastUserTransfer(admin contract.User) {
	client, exist := m.GetConn(admin)
	if exist {
		transfers := repositories.TransferRepo.Get([]*repositories.Where{
			{
				Filed: "to_admin_id = ?",
				Value: admin.GetPrimaryKey(),
			},
			{
				Filed: "is_accepted = ?",
				Value: 0,
			},
			{
				Filed: "is_canceled",
				Value: 0,
			},
		}, -1, []string{"FromAdmin", "User"}, []string{"id desc"})
		data := make([]*resource.ChatTransfer, 0, len(transfers))
		for _, transfer := range transfers {
			data = append(data, transfer.ToJson())
		}
		client.Deliver(NewUserTransfer(data))
	}
}

// 广播在线admin
func (m *adminManager) broadcastAdmins(gid int64) {
	ids := m.GetOnlineUserIds(gid)
	admins := repositories.AdminRepo.Get([]*repositories.Where{{
		Filed: "id in ?",
		Value: ids,
	}}, -1, []string{}, []string{})
	data := make([]resource.Admin, 0, len(admins))
	for _, admin := range admins {
		data = append(data, resource.Admin{
			Avatar:        admin.GetAvatarUrl(),
			Username:      admin.Username,
			Online:        true,
			Id:            admin.GetPrimaryKey(),
			AcceptedCount: chat.AdminService.GetActiveCount(admin.GetPrimaryKey()),
		})
	}
	m.SendAction(NewAdminsAction(data), m.GetAllConn(gid)...)
}
