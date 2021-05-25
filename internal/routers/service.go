package routers

import (
	"fmt"
	"github.com/gin-gonic/gin"
	"ws/internal/http/service"
	service2 "ws/internal/middleware/service"
	"ws/internal/models"
	hub "ws/internal/websocket"
)

func registerService()  {
	g := Router.Group("/service")
	{
		g.POST("/login", service.Login)

		auth := g.Group("/")

		auth.Use(service2.Authenticate)
		auth.GET("/me", service.Me)
		auth.POST("/me/avatar", service.Avatar)
		auth.DELETE("/ws/chat-user/:id", service.RemoveUser)
		auth.POST("/ws/chat-user", service.AcceptUser)
		auth.GET("/ws/chat-users", service.ChatUserList)
		auth.POST("/ws/read-all", service.ReadAll)
		auth.POST("/ws/image", service.Image)
		auth.GET("/ws/messages", service.GetHistoryMessage)

		auth.GET("/ws", func(c *gin.Context) {
			ui, _ := c.Get("user")
			serviceUser := ui.(*models.ServiceUser)
			conn, err := upgrade.Upgrade(c.Writer, c.Request, nil)
			if err != nil {
				fmt.Println(err)
			}
			client := hub.NewServiceConn(serviceUser, conn)
			client.Setup()
			hub.ServiceHub.Login(client)
		})
	}
}