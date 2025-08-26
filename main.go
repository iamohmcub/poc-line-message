package main

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/joho/godotenv"
	"github.com/kelseyhightower/envconfig"
	"github.com/line/line-bot-sdk-go/v7/linebot"
	"github.com/skip2/go-qrcode"
)

/* ---------- โมเดล/สโตร์แบบ in-memory ---------- */

type Session struct {
	UserID        string
	CheckIn       string // "YYYY-MM-DD"
	CheckOut      string // "YYYY-MM-DD"
	Guests        int
	Rooms         int
	RoomType      string
	ReservationID string
	AmountTHB     int
	Status        string // pending | waiting_payment | paid | cancelled
}

var sessions = map[string]*Session{}
var reservations = map[string]*Session{}

var roomPrices = map[string]int{
	"Deluxe Sea View":     800,
	"Ocean Breeze Villa":  4500,
	"The Serenity Resort": 3200,
}

/* ---------- Utilities ---------- */

func mustFlex(jsonStr string) linebot.FlexContainer {
	var raw json.RawMessage = json.RawMessage(jsonStr)
	c, err := linebot.UnmarshalFlexMessageJSON(raw)
	if err != nil {
		log.Println("flex json error:", err)
		return nil
	}
	return c
}

func qrOptions(options []string) *linebot.QuickReplyItems {
	btns := make([]*linebot.QuickReplyButton, 0, len(options))
	for _, o := range options {
		btns = append(btns, linebot.NewQuickReplyButton("", linebot.NewMessageAction(o, o)))
	}
	return linebot.NewQuickReplyItems(btns...)
}

func reply(bot *linebot.Client, token string, msgs ...linebot.SendingMessage) {
	if token == "" {
		return
	}
	if _, err := bot.ReplyMessage(token, msgs...).Do(); err != nil {
		log.Println("reply error:", err)
	}
}

func push(bot *linebot.Client, to string, msgs ...linebot.SendingMessage) {
	if to == "" {
		return
	}
	if _, err := bot.PushMessage(to, msgs...).Do(); err != nil {
		log.Println("push error:", err)
	}
}

func nightsBetween(checkIn, checkOut string) int {
	ci, _ := time.Parse("2006-01-02", checkIn)
	co, _ := time.Parse("2006-01-02", checkOut)
	if co.After(ci) {
		return int(co.Sub(ci).Hours()/24 + 0.5)
	}
	return 0
}

func price(roomType string, nights, rooms int) int {
	base := roomPrices[roomType]
	return base * nights * rooms
}

func buttonsDatePicker(alt, title, data string) *linebot.TemplateMessage {
	tpl := &linebot.ButtonsTemplate{
		Text: title,
		Actions: []linebot.TemplateAction{
			// label, data, mode, initial, max, min
			linebot.NewDatetimePickerAction("เลือกวันที่", data, "date", "", "", ""),
		},
	}
	return linebot.NewTemplateMessage(alt, tpl)
}

/* ---------- สร้าง Flex ---------- */

func roomCarousel() linebot.SendingMessage {
	j := `{
	  "type": "carousel",
	  "contents": [
	    {
	      "type": "bubble",
	      "hero": { "type": "image", "url": "https://images.unsplash.com/photo-1501117716987-c8e97da8f6e7?q=80&w=1600&auto=format&fit=crop", "size":"full","aspectRatio":"20:13","aspectMode":"cover" },
	      "body": { "type":"box","layout":"vertical","spacing":"sm","contents":[
	        { "type":"text","text":"The Serenity Resort","weight":"bold","size":"xl","wrap":true },
	        { "type":"text","text":"เริ่ม ฿3,200 / คืน","size":"lg","weight":"bold","color":"#0E9F6E" },
	        { "type":"text","text":"รีสอร์ทบรรยากาศสงบ เหมาะกับการพักผ่อน","size":"sm","color":"#666666","wrap":true }
	      ]},
	      "footer": { "type":"box","layout":"vertical","spacing":"sm","contents":[
	        { "type":"button","style":"primary","color":"#111827","action":{"type":"postback","label":"เลือก","data":"flow=book&action=pick_room_type&room=The Serenity Resort","displayText":"เลือก The Serenity Resort"} }
	      ]}
	    },
	    {
	      "type": "bubble",
	      "hero": { "type": "image", "url": "https://images.unsplash.com/photo-1542314831-068cd1dbfeeb?q=80&w=1600&auto=format&fit=crop", "size":"full","aspectRatio":"20:13","aspectMode":"cover" },
	      "body": { "type":"box","layout":"vertical","spacing":"sm","contents":[
	        { "type":"text","text":"Ocean Breeze Villa","weight":"bold","size":"xl","wrap":true },
	        { "type":"text","text":"เริ่ม ฿4,500 / คืน","size":"lg","weight":"bold","color":"#0E9F6E" },
	        { "type":"text","text":"วิลล่าริมทะเลพร้อมวิวส่วนตัว","size":"sm","color":"#666666","wrap":true }
	      ]},
	      "footer": { "type":"box","layout":"vertical","spacing":"sm","contents":[
	        { "type":"button","style":"primary","color":"#111827","action":{"type":"postback","label":"เลือก","data":"flow=book&action=pick_room_type&room=Ocean Breeze Villa","displayText":"เลือก Ocean Breeze Villa"} }
	      ]}
	    },
	    {
	      "type": "bubble",
	      "hero": { "type": "image", "url": "https://images.unsplash.com/photo-1496412705862-e0088f16f791?q=80&w=1600&auto=format&fit=crop", "size":"full","aspectRatio":"20:13","aspectMode":"cover" },
	      "body": { "type":"box","layout":"vertical","spacing":"sm","contents":[
	        { "type":"text","text":"Deluxe Sea View","weight":"bold","size":"xl","wrap":true },
	        { "type":"text","text":"เริ่ม ฿800 / คืน","size":"lg","weight":"bold","color":"#0E9F6E" },
	        { "type":"text","text":"ห้องวิวทะเล ราคาคุ้มค่า","size":"sm","color":"#666666","wrap":true }
	      ]},
	      "footer": { "type":"box","layout":"vertical","spacing":"sm","contents":[
	        { "type":"button","style":"primary","color":"#111827","action":{"type":"postback","label":"เลือก","data":"flow=book&action=pick_room_type&room=Deluxe Sea View","displayText":"เลือก Deluxe Sea View"} }
	      ]}
	    }
	  ]
	}`
	if c := mustFlex(j); c != nil {
		return linebot.NewFlexMessage("เลือกห้องพัก", c)
	}
	return linebot.NewTextMessage("แสดงห้องไม่ได้")
}

func summaryCard(s *Session) linebot.SendingMessage {
	n := nightsBetween(s.CheckIn, s.CheckOut)
	j := fmt.Sprintf(`{
	  "type":"bubble",
	  "body":{"type":"box","layout":"vertical","spacing":"md","contents":[
	    {"type":"text","text":"📋 สรุปการจอง","size":"xl","weight":"bold","wrap":true},
	    {"type":"box","layout":"baseline","spacing":"sm","contents":[
	      {"type":"text","text":"ห้อง","size":"sm","color":"#555555"},
	      {"type":"text","text":%q,"size":"sm","weight":"bold","color":"#111827"}
	    ]},
	    {"type":"box","layout":"baseline","spacing":"sm","contents":[
	      {"type":"text","text":"เช็คอิน","size":"sm","color":"#555555"},
	      {"type":"text","text":%q,"size":"sm","weight":"bold","color":"#111827"}
	    ]},
	    {"type":"box","layout":"baseline","spacing":"sm","contents":[
	      {"type":"text","text":"เช็คเอาท์","size":"sm","color":"#555555"},
	      {"type":"text","text":%q,"size":"sm","weight":"bold","color":"#111827"}
	    ]},
	    {"type":"box","layout":"baseline","spacing":"sm","contents":[
	      {"type":"text","text":"คืน","size":"sm","color":"#555555"},
	      {"type":"text","text":%q,"size":"sm","weight":"bold","color":"#111827"}
	    ]},
	    {"type":"box","layout":"baseline","spacing":"sm","contents":[
	      {"type":"text","text":"ผู้เข้าพัก","size":"sm","color":"#555555"},
	      {"type":"text","text":%q,"size":"sm","weight":"bold","color":"#111827"}
	    ]},
	    {"type":"box","layout":"baseline","spacing":"sm","contents":[
	      {"type":"text","text":"ห้อง","size":"sm","color":"#555555"},
	      {"type":"text","text":%q,"size":"sm","weight":"bold","color":"#111827"}
	    ]},
	    {"type":"box","layout":"baseline","spacing":"sm","contents":[
	      {"type":"text","text":"รวม","size":"sm","color":"#555555"},
	      {"type":"text","text":%q,"size":"md","weight":"bold","color":"#0E9F6E"}
	    ]}
	  ]},
	  "footer":{"type":"box","layout":"vertical","spacing":"sm","contents":[
	    {"type":"button","style":"primary","color":"#111827",
	      "action":{"type":"postback","label":"ชำระเงิน","data":"flow=pay&rid=%s","displayText":"ชำระเงิน"} },
	    {"type":"button","action":{"type":"postback","label":"ยกเลิก","data":"flow=cancel&rid=%s","displayText":"ยกเลิก"} }
	  ]}
	}`, s.RoomType, s.CheckIn, s.CheckOut, fmt.Sprintf("%d", n),
		fmt.Sprintf("%d คน", s.Guests), fmt.Sprintf("%d ห้อง", s.Rooms),
		fmt.Sprintf("฿%d", s.AmountTHB), s.ReservationID, s.ReservationID)

	if c := mustFlex(j); c != nil {
		return linebot.NewFlexMessage("สรุปการจอง", c)
	}
	return linebot.NewTextMessage("สรุปการจอง")
}

/* ---------- main ---------- */

type Config struct {
	Port          string `envconfig:"PORT"`
	ChannelSecret string `envconfig:"LINE_CHANNEL_SECRET"`
	ChannelToken  string `envconfig:"LINE_CHANNEL_TOKEN"`
}

var cfg Config

func init() {
	_ = godotenv.Load()
	if err := envconfig.Process("", &cfg); err != nil {
		log.Fatalf("read env error : %s", err.Error())
	}
}
func main() {
	secret := cfg.ChannelSecret
	token := cfg.ChannelToken
	if secret == "" || token == "" {
		log.Fatal("set CHANNEL_SECRET and CHANNEL_TOKEN")
	}
	bot, err := linebot.New(secret, token)
	if err != nil {
		log.Fatal(err)
	}

	// เสิร์ฟ QR (เดโม่)
	http.HandleFunc("/qr/", func(w http.ResponseWriter, r *http.Request) {
		rid := strings.TrimPrefix(r.URL.Path, "/qr/")
		s := reservations[rid]
		if s == nil {
			http.NotFound(w, r)
			return
		}
		payload := fmt.Sprintf("PROMPTPAY-DEMO|RID=%s|AMOUNT=%d", rid, s.AmountTHB)
		png, _ := qrcode.Encode(payload, qrcode.Medium, 256)
		w.Header().Set("Content-Type", "image/png")
		w.Write(png)
	})

	http.HandleFunc("/webhook", func(w http.ResponseWriter, r *http.Request) {
		events, err := bot.ParseRequest(r)
		if err != nil {
			if err == linebot.ErrInvalidSignature {
				http.Error(w, "bad signature", http.StatusBadRequest)
				return
			}
			log.Println("parse:", err)
			w.WriteHeader(http.StatusOK)
			w.Write([]byte("OK"))
			return
		}

		for _, ev := range events {
			userID := ""
			if ev.Source != nil {
				userID = ev.Source.UserID
			}

			switch ev.Type {

			case linebot.EventTypeMessage:
				switch m := ev.Message.(type) {
				case *linebot.ImageMessage:
					// รับสลิป → ปิดงานเป็น paid (เดโม่)
					s := sessions[userID]
					if s != nil && s.Status == "waiting_payment" {
						s.Status = "paid"
						reply(bot, ev.ReplyToken, linebot.NewTextMessage("✅ รับสลิปแล้ว ขอบคุณครับ! การจองของคุณยืนยันเรียบร้อย 🎉"))
					} else {
						reply(bot, ev.ReplyToken, linebot.NewTextMessage("ส่งรูปมาแล้วครับ"))
					}

				case *linebot.TextMessage:
					text := strings.TrimSpace(m.Text)
					lower := strings.ToLower(text)

					if lower == "เมนู" || lower == "start" || strings.Contains(lower, "จองห้อง") {
						msg := linebot.NewTextMessage("เริ่มจองห้อง: เลือกวันที่เช็คอิน")
						msg = msg.WithQuickReplies(qrOptions([]string{"เมนู", "ติดต่อโรงแรม"})).(*linebot.TextMessage)
						reply(bot, ev.ReplyToken, buttonsDatePicker("เลือกเช็คอิน", "คุณต้องการเช็คอินวันไหน?", "flow=book&action=checkin"))
						continue
					}

					// เลือกจำนวนผู้เข้าพัก (จากข้อความ)
					if strings.HasSuffix(text, " คน") {
						num := strings.TrimSuffix(text, " คน")
						if n, err := strconv.Atoi(strings.TrimSpace(num)); err == nil {
							s := sessions[userID]
							if s != nil && s.CheckIn != "" && s.CheckOut != "" {
								s.Guests = n
								// ถามจำนวนห้อง
								var opts []string
								for i := 1; i <= 3; i++ {
									opts = append(opts, fmt.Sprintf("%d ห้อง", i))
								}
								reply(bot, ev.ReplyToken, linebot.NewTextMessage("ต้องการกี่ห้อง?").WithQuickReplies(qrOptions(opts)))
								continue
							}
						}
					}

					// เลือกจำนวนห้อง (จากข้อความ)
					if strings.HasSuffix(text, " ห้อง") {
						num := strings.TrimSuffix(text, " ห้อง")
						if n, err := strconv.Atoi(strings.TrimSpace(num)); err == nil {
							s := sessions[userID]
							if s != nil && s.CheckIn != "" && s.CheckOut != "" && s.Guests > 0 {
								s.Rooms = n
								// ส่งคาโรเซลให้เลือกประเภทห้อง
								reply(bot, ev.ReplyToken, roomCarousel())
								continue
							}
						}
					}

					// ไม่รู้จักคำสั่ง → ชี้แนะ
					reply(bot, ev.ReplyToken, linebot.NewTextMessage("พิมพ์ “เมนู” เพื่อเริ่มจองห้อง 😀"))
				}

			case linebot.EventTypePostback:
				data := ev.Postback.Data
				date := ""
				if ev.Postback.Params != nil {
					date = ev.Postback.Params.Date
				}

				// เริ่มจอง: เช็คอิน
				if strings.Contains(data, "flow=book&action=checkin") && date != "" {
					if sessions[userID] == nil {
						sessions[userID] = &Session{UserID: userID, Status: "pending"}
					}
					sessions[userID].CheckIn = date
					// ขอเช็คเอาท์
					push(bot, userID, linebot.NewTextMessage("คุณเลือกเช็คอิน: "+date+"\nต่อไปเลือกวันที่เช็คเอาท์"))
					push(bot, userID, buttonsDatePicker("เลือกเช็คเอาท์", "คุณต้องการเช็คเอาท์วันไหน?", "flow=book&action=checkout"))
					continue
				}

				// เช็คเอาท์
				if strings.Contains(data, "flow=book&action=checkout") && date != "" {
					s := sessions[userID]
					if s == nil {
						continue
					}
					s.CheckOut = date
					push(bot, userID, linebot.NewTextMessage("เช็คเอาท์: "+date))

					// ถามผู้เข้าพัก (Quick Reply)
					var opts []string
					for i := 1; i <= 4; i++ {
						opts = append(opts, fmt.Sprintf("%d คน", i))
					}
					push(bot, userID, linebot.NewTextMessage("จำนวนผู้เข้าพัก?").WithQuickReplies(qrOptions(opts)))
					continue
				}

				// เลือกประเภทห้อง
				if strings.Contains(data, "flow=book&action=pick_room_type") {
					s := sessions[userID]
					if s == nil {
						continue
					}
					// ดึงชื่อห้องจาก data
					if strings.Contains(data, "room=") {
						room := data[strings.Index(data, "room=")+5:]
						s.RoomType = room
						// คำนวณราคา & ออกเลขจอง
						n := nightsBetween(s.CheckIn, s.CheckOut)
						if n <= 0 {
							n = 1
						}
						s.AmountTHB = price(s.RoomType, n, s.Rooms)
						s.ReservationID = fmt.Sprintf("R-%d", time.Now().UnixNano())
						reservations[s.ReservationID] = s
						// แสดงสรุป
						reply(bot, ev.ReplyToken, summaryCard(s))
					}
					continue
				}

				// ชำระเงิน -> ส่ง QR + set waiting_payment
				if strings.Contains(data, "flow=pay&rid=") {
					rid := data[strings.Index(data, "rid=")+4:]
					s := reservations[rid]
					if s == nil {
						continue
					}
					s.Status = "waiting_payment"
					qrURL := fmt.Sprintf("https://%s/qr/%s", r.Host, rid)
					push(bot, userID, linebot.NewImageMessage(qrURL, qrURL))
					push(bot, userID, linebot.NewTextMessage("สแกน QR เพื่อชำระเงิน แล้วอัปโหลดสลิปในแชตนี้ได้เลยครับ"))
					continue
				}

				// ยกเลิก
				if strings.Contains(data, "flow=cancel&rid=") {
					rid := data[strings.Index(data, "rid=")+4:]
					if s := reservations[rid]; s != nil {
						s.Status = "cancelled"
					}
					reply(bot, ev.ReplyToken, linebot.NewTextMessage("ยกเลิกการจองแล้วครับ"))
					continue
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
