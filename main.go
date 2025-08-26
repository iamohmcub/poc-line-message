package main

import (
	"encoding/json"
	"fmt"
	"log"
	"math"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/line/line-bot-sdk-go/v7/linebot"
)

/*
ENV ที่ต้องตั้งค่า
- LINE_CHANNEL_SECRET
- LINE_CHANNEL_TOKEN
- PORT (เช่น 8080)
- PROMPTPAY_ID (เช่น เบอร์มือถือ 0812345678 หรือ Tax ID 13 หลัก)
*/

func main() {
	secret := mustGetenv("LINE_CHANNEL_SECRET")
	token := mustGetenv("LINE_CHANNEL_TOKEN")
	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}

	bot, err := linebot.New(secret, token)
	if err != nil {
		log.Fatalf("linebot.New error: %v", err)
	}

	// Webhook
	http.HandleFunc("/callback", func(w http.ResponseWriter, r *http.Request) {
		events, err := bot.ParseRequest(r)
		if err != nil {
			if err == linebot.ErrInvalidSignature {
				http.Error(w, "bad signature", http.StatusBadRequest)
			} else {
				http.Error(w, "parse error", http.StatusInternalServerError)
			}
			log.Printf("ParseRequest: %v", err)
			return
		}
		for _, ev := range events {
			switch ev.Type {

			case linebot.EventTypeMessage:
				switch m := ev.Message.(type) {
				case *linebot.TextMessage:
					onText(bot, ev, m.Text)
				default:
					reply(bot, ev.ReplyToken, linebot.NewTextMessage("ตอนนี้รองรับเฉพาะข้อความตัวอักษรนะครับ 😊"))
				}

			case linebot.EventTypePostback:
				onPostback(bot, ev)

			default:
				// ignore
			}
		}
	})

	// เดโม: endpoint สำหรับ “แจ้งชำระเงินสำเร็จ” จากระบบหลังบ้าน/เพจเกตเวย์ → จะทำการ Push ยืนยันการจอง
	// วิธีทดสอบ: GET /dev/paid?resv=R-xxxxx
	http.HandleFunc("/dev/paid", func(w http.ResponseWriter, r *http.Request) {
		resvID := r.URL.Query().Get("resv")
		if resvID == "" {
			http.Error(w, "missing resv", http.StatusBadRequest)
			return
		}
		sess := findSessionByReservation(resvID)
		if sess == nil {
			http.Error(w, "reservation not found", http.StatusNotFound)
			return
		}
		// อัปเดตสถานะ
		sessMu.Lock()
		sess.Paid = true
		sessMu.Unlock()

		pushReservationConfirmed(bot, sess)
		w.Write([]byte("OK"))
	})

	log.Printf("listening on :%s ...", port)
	log.Fatal(http.ListenAndServe(":"+port, nil))
}

/* ---------- State / Domain ---------- */

type Session struct {
	UserID      string
	Stage       string // people -> room -> checkin -> checkout -> price -> awaiting_payment -> done
	Adults      int
	Children    int
	RoomID      string
	RoomName    string
	CheckIn     time.Time
	CheckOut    time.Time
	Nights      int
	PricePerN   float64
	Amount      float64
	Reservation string
	Paid        bool
}

var (
	sessMu   sync.RWMutex
	sessions = map[string]*Session{}
)

// mock room catalog/price/capacity
type Room struct {
	ID        string
	Name      string
	CapacityA int // max adults
	CapacityC int // max children (แนะนำ)
	PricePerN float64
}

var rooms = []Room{
	{ID: "STD", Name: "Standard (2 pax)", CapacityA: 2, CapacityC: 1, PricePerN: 1000},
	{ID: "TRP", Name: "Triple (3 pax)", CapacityA: 3, CapacityC: 1, PricePerN: 1300},
	{ID: "FAM", Name: "Family (2+2)", CapacityA: 2, CapacityC: 2, PricePerN: 1500},
	{ID: "STE", Name: "Suite (4 pax)", CapacityA: 4, CapacityC: 2, PricePerN: 2200},
}

func fits(r Room, a, c int) bool {
	return a <= r.CapacityA && c <= r.CapacityC && (a+c) > 0
}

/* ---------- Handlers ---------- */

func onText(bot *linebot.Client, ev *linebot.Event, text string) {
	userID := ev.Source.UserID
	sess := getOrCreateSession(userID)

	low := strings.ToLower(strings.TrimSpace(text))

	// คำสั่งเริ่มจอง
	if strings.Contains(low, "จอง") || strings.Contains(low, "book") {
		resetSession(sess)
		sess.Stage = "people"
		replyAskPeople(bot, ev.ReplyToken)
		return
	}

	// รองรับพิมพ์รูปแบบ "ผู้ใหญ่2เด็ก1" หรือ "a2c1" แบบเร็ว ๆ
	if sess.Stage == "people" {
		if a, c, ok := parsePeople(low); ok {
			sessMu.Lock()
			sess.Adults, sess.Children = a, c
			sessMu.Unlock()
			replyRoomType(bot, ev.ReplyToken, a, c)
			sessMu.Lock()
			sess.Stage = "room"
			sessMu.Unlock()
			return
		}
		// ไม่เข้า format → ส่ง quick reply ให้เลือก
		replyAskPeople(bot, ev.ReplyToken)
		return
	}

	// สำหรับเดโม: ผู้ใช้พิมพ์ "ชำระแล้ว" เพื่อจำลองการจ่ายเงิน
	if sess.Stage == "awaiting_payment" && (low == "ชำระแล้ว" || low == "paid") {
		sessMu.Lock()
		sess.Paid = true
		sessMu.Unlock()
		pushReservationConfirmed(bot, sess)
		return
	}

	// default help
	reply(bot, ev.ReplyToken, linebot.NewTextMessage("พิมพ์ “จองห้อง” เพื่อเริ่มการจองครับ"))
}

func onPostback(bot *linebot.Client, ev *linebot.Event) {
	userID := ev.Source.UserID
	sess := getOrCreateSession(userID)

	data := ev.Postback.Data
	params := ev.Postback.Params

	// DatetimePicker - เลือกวันที่
	if params != nil && params.Date != "" {
		switch {
		case strings.Contains(data, "action=set_checkin"):
			t, _ := time.Parse("2006-01-02", params.Date)
			sessMu.Lock()
			sess.CheckIn = t
			sess.Stage = "checkin_done"
			sessMu.Unlock()
			replyAskCheckout(bot, ev.ReplyToken, t)
			return

		case strings.Contains(data, "action=set_checkout"):
			t, _ := time.Parse("2006-01-02", params.Date)
			sessMu.Lock()
			sess.CheckOut = t
			n := int(math.Ceil(sess.CheckOut.Sub(sess.CheckIn).Hours() / 24.0))
			if n < 1 {
				n = 1
			}
			sess.Nights = n
			// คำนวณราคา
			sess.Amount = float64(sess.Nights) * sess.PricePerN
			sess.Stage = "price"
			sessMu.Unlock()

			replyPriceSummary(bot, ev.ReplyToken, sess)
			return
		}
	}

	// Postback action ทั่วไป
	switch {
	case strings.HasPrefix(data, "action=set_people"):
		// action=set_people&a=2&c=1
		q, _ := url.ParseQuery(strings.TrimPrefix(data, "action=set_people&"))
		a, _ := strconv.Atoi(q.Get("a"))
		c, _ := strconv.Atoi(q.Get("c"))

		sessMu.Lock()
		sess.Adults, sess.Children = a, c
		sess.Stage = "room"
		sessMu.Unlock()

		replyRoomType(bot, ev.ReplyToken, a, c)

	case strings.HasPrefix(data, "action=choose_room"):
		// action=choose_room&room=STD
		q, _ := url.ParseQuery(strings.TrimPrefix(data, "action=choose_room&"))
		roomID := q.Get("room")
		r := getRoom(roomID)
		if r == nil {
			reply(bot, ev.ReplyToken, linebot.NewTextMessage("ไม่พบประเภทห้องที่เลือก กรุณาลองใหม่"))
			return
		}

		sessMu.Lock()
		sess.RoomID = r.ID
		sess.RoomName = r.Name
		sess.PricePerN = r.PricePerN
		sess.Stage = "checkin"
		sessMu.Unlock()

		replyAskCheckin(bot, ev.ReplyToken)

	case strings.HasPrefix(data, "action=confirm"):
		// สร้าง Reservation + ส่ง QR จ่ายเงิน
		sessMu.Lock()
		sess.Reservation = fmt.Sprintf("R-%d", time.Now().UnixNano())
		sess.Stage = "awaiting_payment"
		sessMu.Unlock()

		replyPromptPayQR(bot, ev.ReplyToken, sess)

	case strings.HasPrefix(data, "action=restart"):
		resetSession(sess)
		sessMu.Lock()
		sess.Stage = "people"
		sessMu.Unlock()
		replyAskPeople(bot, ev.ReplyToken)

	default:
		reply(bot, ev.ReplyToken, linebot.NewTextMessage("รับข้อมูลแล้วครับ"))
	}
}

/* ---------- Replies ---------- */

func replyAskPeople(bot *linebot.Client, replyToken string) {
	txt := linebot.NewTextMessage("ต้องการพักกี่คนครับ? (เช่น ผู้ใหญ่2เด็ก1) หรือกดเลือกด้านล่าง")
	qr := linebot.NewQuickReplyItems(
		linebot.NewQuickReplyButton("", linebot.NewPostbackAction("ผู้ใหญ่1 เด็ก0", "action=set_people&a=1&c=0", "", "ผู้ใหญ่1 เด็ก0", "", "")),
		linebot.NewQuickReplyButton("", linebot.NewPostbackAction("ผู้ใหญ่2 เด็ก0", "action=set_people&a=2&c=0", "", "ผู้ใหญ่2 เด็ก0", "", "")),
		linebot.NewQuickReplyButton("", linebot.NewPostbackAction("ผู้ใหญ่2 เด็ก1", "action=set_people&a=2&c=1", "", "ผู้ใหญ่2 เด็ก1", "", "")),
		linebot.NewQuickReplyButton("", linebot.NewPostbackAction("ผู้ใหญ่2 เด็ก2", "action=set_people&a=2&c=2", "", "ผู้ใหญ่2 เด็ก2", "", "")),
	)
	reply(bot, replyToken, txt.WithQuickReplies(qr))
}

func replyRoomType(bot *linebot.Client, replyToken string, a, c int) {
	// สร้าง Template Carousel ของประเภทห้องที่รองรับจำนวนคน
	var cols []*linebot.CarouselColumn
	for _, r := range rooms {
		if !fits(r, a, c) {
			continue
		}
		btn := linebot.NewPostbackAction("เลือก", "action=choose_room&room="+r.ID, "", "เลือก", "", "")
		col := linebot.NewCarouselColumn(
			"", r.Name, fmt.Sprintf("รองรับ ผู้ใหญ่%d เด็ก%d\n฿%.0f/คืน", r.CapacityA, r.CapacityC, r.PricePerN),
			btn,
		)
		cols = append(cols, col)
	}
	if len(cols) == 0 {
		reply(bot, replyToken, linebot.NewTextMessage("ไม่มีประเภทห้องที่รองรับจำนวนคนนี้ ลองเปลี่ยนจำนวนผู้เข้าพักหรือติดต่อโรงแรมครับ"))
		return
	}
	tmpl := linebot.NewCarouselTemplate(cols...)
	reply(bot, replyToken, linebot.NewTemplateMessage("เลือกประเภทห้อง", tmpl))
}

func replyAskCheckin(bot *linebot.Client, replyToken string) {
	today := time.Now().In(bangkok())
	min := today.Format("2006-01-02")
	max := today.AddDate(0, 6, 0).Format("2006-01-02")

	// ปุ่มเปิด Date Picker (Check-in)
	btn := linebot.NewDatetimePickerAction("เลือกเช็คอิน", "action=set_checkin", string(linebot.ActionTypeDatetimePicker), today.Format("2006-01-02"), min, max)

	tmpl := linebot.NewButtonsTemplate(
		"", "เลือกวันที่เช็คอิน", "กรุณาเลือกวันที่เช็คอิน",
		btn,
	)
	reply(bot, replyToken, linebot.NewTemplateMessage("เลือกวันที่เช็คอิน", tmpl))
}

func replyAskCheckout(bot *linebot.Client, replyToken string, checkIn time.Time) {
	min := checkIn.AddDate(0, 0, 1).Format("2006-01-02")
	max := checkIn.AddDate(0, 0, 30).Format("2006-01-02")
	initial := min

	btn := linebot.NewDatetimePickerAction("เลือกเช็คเอาท์", "action=set_checkout", string(linebot.ActionTypeDatetimePicker), initial, min, max)

	tmpl := linebot.NewButtonsTemplate(
		"", "เลือกวันที่เช็คเอาท์", fmt.Sprintf("เช็คอิน %s\nกรุณาเลือกเช็คเอาท์", checkIn.Format("02 Jan 2006")),
		btn,
	)
	reply(bot, replyToken, linebot.NewTemplateMessage("เลือกวันที่เช็คเอาท์", tmpl))
}

func replyPriceSummary(bot *linebot.Client, replyToken string, s *Session) {
	// Flex แสดงสรุป + ปุ่มยืนยัน
	body := &linebot.BoxComponent{
		Type:   linebot.FlexComponentTypeBox,
		Layout: linebot.FlexBoxLayoutTypeVertical,
		Contents: []linebot.FlexComponent{
			&linebot.TextComponent{Type: linebot.FlexComponentTypeText, Text: "สรุปการจอง", Weight: linebot.FlexTextWeightTypeBold, Size: linebot.FlexTextSizeTypeXl},
			&linebot.SeparatorComponent{Type: linebot.FlexComponentTypeSeparator, Margin: "md"},
			&linebot.TextComponent{Type: linebot.FlexComponentTypeText, Text: "ห้อง: " + s.RoomName, Wrap: true, Margin: "md"},
			&linebot.TextComponent{Type: linebot.FlexComponentTypeText, Text: fmt.Sprintf("ผู้ใหญ่ %d   เด็ก %d", s.Adults, s.Children), Wrap: true},
			&linebot.TextComponent{Type: linebot.FlexComponentTypeText, Text: fmt.Sprintf("เช็คอิน:  %s", s.CheckIn.Format("02 Jan 2006"))},
			&linebot.TextComponent{Type: linebot.FlexComponentTypeText, Text: fmt.Sprintf("เช็คเอาท์: %s", s.CheckOut.Format("02 Jan 2006"))},
			&linebot.TextComponent{Type: linebot.FlexComponentTypeText, Text: fmt.Sprintf("จำนวนคืน: %d", s.Nights)},
			&linebot.TextComponent{Type: linebot.FlexComponentTypeText, Text: fmt.Sprintf("ราคา/คืน: ฿%.0f", s.PricePerN)},
			&linebot.TextComponent{Type: linebot.FlexComponentTypeText, Text: fmt.Sprintf("รวมทั้งสิ้น: ฿%.0f", s.Amount), Weight: linebot.FlexTextWeightTypeBold, Margin: "md"},
		},
	}

	footer := &linebot.BoxComponent{
		Type:   linebot.FlexComponentTypeBox,
		Layout: linebot.FlexBoxLayoutTypeVertical,
		Contents: []linebot.FlexComponent{
			&linebot.ButtonComponent{
				Type:   linebot.FlexComponentTypeButton,
				Style:  linebot.FlexButtonStyleTypePrimary,
				Action: linebot.NewPostbackAction("ยืนยันการจอง", "action=confirm", "", "ยืนยันการจอง", "", ""),
			},
			&linebot.ButtonComponent{
				Type:   linebot.FlexComponentTypeButton,
				Margin: "sm",
				Action: linebot.NewPostbackAction("เริ่มใหม่", "action=restart", "", "เริ่มใหม่", "", ""),
			},
		},
	}

	bubble := &linebot.BubbleContainer{
		Type:   linebot.FlexContainerTypeBubble,
		Body:   body,
		Footer: footer,
	}
	reply(bot, replyToken, linebot.NewFlexMessage("สรุปการจอง", bubble))
}

func replyPromptPayQR(bot *linebot.Client, replyToken string, s *Session) {
	id := mustGetenv("PROMPTPAY_ID")                          // เบอร์/TaxID ของโรงแรม
	payload, err := BuildPromptPayPayload(id, s.Amount, true) // dynamic=true
	if err != nil {
		reply(bot, replyToken, linebot.NewTextMessage("สร้าง QR ชำระเงินไม่สำเร็จ กรุณาลองใหม่"))
		return
	}

	// สำหรับเดโม ใช้บริการสร้างรูป QR ชั่วคราว (โปรดเปลี่ยนเป็นการ host รูปเองในโปรดักชัน)
	qrURL := "https://api.qrserver.com/v1/create-qr-code/?size=600x600&data=" + url.QueryEscape(payload)

	alt := fmt.Sprintf("ชำระเงิน ฿%.0f, หมายเลขจอง %s", s.Amount, s.Reservation)
	img := linebot.NewImageMessage(qrURL, qrURL)
	txt := linebot.NewTextMessage(alt + "\n\nหลังชำระแล้วพิมพ์ “ชำระแล้ว” หรือให้ระบบชำระเงินยิง /dev/paid?resv=" + s.Reservation)

	reply(bot, replyToken, img, txt)
}

/* ---------- Push confirm (หลังชำระสำเร็จ) ---------- */

func pushReservationConfirmed(bot *linebot.Client, s *Session) {
	if s == nil {
		return
	}
	card := confirmFlex(s)
	if _, err := bot.PushMessage(s.UserID, card).Do(); err != nil {
		log.Printf("push confirm error: %v", err)
	}
}

func confirmFlex(s *Session) *linebot.FlexMessage {
	body := &linebot.BoxComponent{
		Type:   linebot.FlexComponentTypeBox,
		Layout: linebot.FlexBoxLayoutTypeVertical,
		Contents: []linebot.FlexComponent{
			&linebot.TextComponent{Type: linebot.FlexComponentTypeText, Text: "ยืนยันการจองเรียบร้อย ✅", Weight: linebot.FlexTextWeightTypeBold, Size: linebot.FlexTextSizeTypeXl},
			&linebot.SeparatorComponent{Type: linebot.FlexComponentTypeSeparator, Margin: "md"},
			&linebot.TextComponent{Type: linebot.FlexComponentTypeText, Text: "หมายเลขจอง: " + s.Reservation, Margin: "md"},
			&linebot.TextComponent{Type: linebot.FlexComponentTypeText, Text: fmt.Sprintf("ห้อง: %s", s.RoomName)},
			&linebot.TextComponent{Type: linebot.FlexComponentTypeText, Text: fmt.Sprintf("เช็คอิน:  %s", s.CheckIn.Format("02 Jan 2006"))},
			&linebot.TextComponent{Type: linebot.FlexComponentTypeText, Text: fmt.Sprintf("เช็คเอาท์: %s", s.CheckOut.Format("02 Jan 2006"))},
			&linebot.TextComponent{Type: linebot.FlexComponentTypeText, Text: fmt.Sprintf("ยอดที่ชำระ: ฿%.0f", s.Amount), Weight: linebot.FlexTextWeightTypeBold, Margin: "md"},
		},
	}
	return linebot.NewFlexMessage("ยืนยันการจอง", &linebot.BubbleContainer{Type: linebot.FlexContainerTypeBubble, Body: body})
}

/* ---------- Utils ---------- */

func mustGetenv(k string) string {
	v := os.Getenv(k)
	if v == "" {
		log.Fatalf("missing env: %s", k)
	}
	return v
}

func getOrCreateSession(userID string) *Session {
	sessMu.Lock()
	defer sessMu.Unlock()
	s, ok := sessions[userID]
	if !ok {
		s = &Session{UserID: userID, Stage: "idle"}
		sessions[userID] = s
	}
	return s
}
func resetSession(s *Session) {
	sessMu.Lock()
	defer sessMu.Unlock()
	*s = Session{UserID: s.UserID, Stage: "idle"}
}

// หาจำนวนคนจากข้อความ เช่น "ผู้ใหญ่2เด็ก1", "a2c1", "adults2 children1"
var rePeople = regexp.MustCompile(`(?:(?:ผู้ใหญ่|adult|adults|a)\s*([0-9]+)).*(?:(?:เด็ก|child|children|c)\s*([0-9]+))`)

func parsePeople(s string) (int, int, bool) {
	m := rePeople.FindStringSubmatch(s)
	if len(m) == 3 {
		a, _ := strconv.Atoi(m[1])
		c, _ := strconv.Atoi(m[2])
		return a, c, true
	}
	return 0, 0, false
}

func getRoom(id string) *Room {
	for i := range rooms {
		if rooms[i].ID == id {
			return &rooms[i]
		}
	}
	return nil
}

func reply(bot *linebot.Client, replyToken string, msgs ...linebot.SendingMessage) {
	if _, err := bot.ReplyMessage(replyToken, msgs...).Do(); err != nil {
		log.Printf("Reply error: %v", err)
	}
}

func bangkok() *time.Location {
	loc, _ := time.LoadLocation("Asia/Bangkok")
	return loc
}

/* ---------- PromptPay QR (EMVCo) ---------- */

// BuildPromptPayPayload สร้าง EMVCo payload สำหรับ PromptPay
// id = เบอร์มือถือไทย (เช่น 0812345678) หรือ Tax ID 13 หลัก
// amount = ยอดเป็นบาท, dynamic = ใช้ Point of Initiation = 12 (ใช้ครั้งเดียว, กำหนดจำนวนเงิน)
func BuildPromptPayPayload(id string, amount float64, dynamic bool) (string, error) {
	target, tag01 := normalizePromptPayID(id)
	if target == "" {
		return "", fmt.Errorf("invalid PROMPTPAY_ID")
	}
	// TLV helpers
	tlv := func(tag string, value string) string {
		return tag + fmt.Sprintf("%02d", len(value)) + value
	}

	// 00 Payload Format Indicator
	payload := tlv("00", "01")
	// 01 Point of Initiation Method
	if dynamic {
		payload += tlv("01", "12")
	} else {
		payload += tlv("01", "11")
	}

	// 26 Merchant Account Information (PromptPay AID + subfields)
	aid := tlv("00", "A000000677010111")
	acc := tlv(tag01, target) // "01"=mobile, "02"=TaxID, "03"=E-Wallet
	payload += tlv("26", aid+acc)

	// 52 MCC (unknown/peer2peer)
	payload += tlv("52", "0000")
	// 53 Currency (764 = THB)
	payload += tlv("53", "764")
	// 54 Amount
	if amount > 0 {
		payload += tlv("54", fmt.Sprintf("%.2f", amount))
	}
	// 58 Country Code
	payload += tlv("58", "TH")
	// 59 Merchant Name (optional, ใช้สั้น ๆ)
	payload += tlv("59", "HOTEL")
	// 60 City
	payload += tlv("60", "BANGKOK")

	// 63 CRC (ใส่ "63" + "04" + CRC16)
	crc := strings.ToUpper(crc16(payload + "6304"))
	payload += tlv("63", crc)
	return payload, nil
}

// คืนค่า: normalized, tag ("01" mobile / "02" taxid)
func normalizePromptPayID(id string) (string, string) {
	s := strings.TrimSpace(strings.ReplaceAll(id, "-", ""))
	// Mobile: 10 digits, เริ่มด้วย 0 → แปลงเป็น 0066 + no leading 0
	if matched, _ := regexp.MatchString(`^0[0-9]{9}$`, s); matched {
		return "0066" + s[1:], "01"
	}
	// Tax ID: 13 digits
	if matched, _ := regexp.MatchString(`^[0-9]{13}$`, s); matched {
		return s, "02"
	}
	// E-Wallet (15)
	if matched, _ := regexp.MatchString(`^[0-9]{15}$`, s); matched {
		return s, "03"
	}
	return "", ""
}

// CRC16-CCITT (0x1021), initial 0xFFFF
func crc16(s string) string {
	data := []byte(s)
	var polynomial uint16 = 0x1021
	var crc uint16 = 0xFFFF
	for _, b := range data {
		crc ^= uint16(b) << 8
		for i := 0; i < 8; i++ {
			if (crc & 0x8000) != 0 {
				crc = (crc << 1) ^ polynomial
			} else {
				crc <<= 1
			}
		}
	}
	return fmt.Sprintf("%04x", crc&0xFFFF)
}

/* ---------- Dev helpers ---------- */

func findSessionByReservation(resv string) *Session {
	sessMu.RLock()
	defer sessMu.RUnlock()
	for _, s := range sessions {
		if s.Reservation == resv {
			return s
		}
	}
	return nil
}

/* ---------- (Optional) JSON debug ---------- */
func toJSON(v any) string {
	b, _ := json.MarshalIndent(v, "", "  ")
	return string(b)
}
