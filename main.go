package main

import (
	"log"
	"net/http"
	"os"
	"strings"

	"github.com/line/line-bot-sdk-go/v7/linebot"
)

// สร้าง Quick Reply จากลิสต์ข้อความ (กดแล้วจะส่งข้อความนั้นกลับมา)
func qrOptions(options []string) *linebot.QuickReplyItems {
	btns := make([]*linebot.QuickReplyButton, 0, len(options))
	for _, o := range options {
		btns = append(btns, linebot.NewQuickReplyButton("", linebot.NewMessageAction(o, o)))
	}
	return linebot.NewQuickReplyItems(btns...)
}

func main() {
	secret := os.Getenv("CHANNEL_SECRET")
	token := os.Getenv("CHANNEL_TOKEN")
	if secret == "" || token == "" {
		log.Fatal("set env CHANNEL_SECRET and CHANNEL_TOKEN first")
	}

	bot, err := linebot.New(secret, token)
	if err != nil {
		log.Fatal(err)
	}

	http.HandleFunc("/webhook", func(w http.ResponseWriter, r *http.Request) {
		events, err := bot.ParseRequest(r)
		if err != nil {
			if err == linebot.ErrInvalidSignature {
				http.Error(w, "bad signature", http.StatusBadRequest)
				return
			}
			log.Println("parse error:", err)
			w.WriteHeader(http.StatusOK)
			w.Write([]byte("OK"))
			return
		}

		for _, ev := range events {
			switch ev.Type {
			case linebot.EventTypeMessage:
				if m, ok := ev.Message.(*linebot.TextMessage); ok {
					text := strings.TrimSpace(m.Text)
					lower := strings.ToLower(text)

					// พิมพ์ "เมนู" หรือ "start" เพื่อแสดงตัวเลือก
					if lower == "เมนู" || lower == "start" {
						msg := linebot.
							NewTextMessage("เลือกเมนูที่ต้องการได้เลย:")
						msgWithQuickReplies := msg.WithQuickReplies(qrOptions([]string{
							"ค้นหาห้อง", "การจองของฉัน", "คุยกับโรงแรม",
						}))
						if _, err := bot.ReplyMessage(ev.ReplyToken, msgWithQuickReplies).Do(); err != nil {
							log.Println("reply error:", err)
						}
						continue
					}

					// เมื่อผู้ใช้แตะตัวเลือกใด ๆ (จะส่งเป็นข้อความกลับมา)
					switch text {
					case "ค้นหาห้อง":
						bot.ReplyMessage(ev.ReplyToken, linebot.NewTextMessage("โอเค! เดี๋ยวหาห้องให้ 😊\n(เดโม: ยังไม่ได้เชื่อมข้อมูลจริง)")).Do()
					case "การจองของฉัน":
						bot.ReplyMessage(ev.ReplyToken, linebot.NewTextMessage("คุณยังไม่มีการจองล่าสุด\n(เดโม)")).Do()
					case "คุยกับโรงแรม":
						bot.ReplyMessage(ev.ReplyToken, linebot.NewTextMessage("พิมพ์ข้อความถึงโรงแรมได้เลยครับ 👇")).Do()
					default:
						// แนะนำให้พิมพ์ "เมนู" ถ้าไม่รู้จักคำสั่ง
						bot.ReplyMessage(ev.ReplyToken, linebot.NewTextMessage("พิมพ์ “เมนู” เพื่อเริ่มต้น")).Do()
					}
				}
			}
		}

		w.WriteHeader(http.StatusOK)
		w.Write([]byte("OK"))
	})

	port := os.Getenv("PORT")
	if port == "" {
		port = "3000"
	}
	log.Println("listening on :" + port)
	log.Fatal(http.ListenAndServe(":"+port, nil))
}
