package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/go-redis/redis/v8"
	"github.com/google/uuid"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
	"sms-challenge.com/configs"
	"sms-challenge.com/models"
)

var (
	rdb *redis.Client
	ctx context.Context
	db  *gorm.DB
)

var decCreditScripr = redis.NewScript(`
local balance = redis.call('GET',KEYS[1]) if not balance then return {err='no_balance'} end local bal=tonumber(balance) local req=tonumber(ARGV[1]) local used=0 if req>=bal then used=bal redis.call('DEL',KEYS[1]) else used=req redis.call('SET',KEYS[1],bal-req) end 
return used
`)

var refundScript = redis.NewScript(`
	if redis.call("EXISTS",KEYS[1])==1 then
		redis.call("INCRBY",KEYS[1],tonumber(ARGV[1]))
	    return "refunded_existing_key"
	else
		redis.call("SET",KEYS[1],tonumber(ARGV[1]))

		return "refunded_new_key"
	end
`)

func initDB() *gorm.DB {
	host := "localhost"
	port := 5432
	user := "postgres"
	password := "yourStrongPassword"
	dbname := "trafficdb"

	dsn := fmt.Sprintf(
		"host=%s port=%d user=%s password=%s dbname=%s sslmode=disable TimeZone=UTC",
		host, port, user, password, dbname,
	)

	db, err := gorm.Open(postgres.Open(dsn), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Info),
	})
	if err != nil {
		log.Fatalf("failed to connect")
	}

	log.Println("connected to db postgres")
	return db
}

func main() {
	fmt.Printf("hello sms")
	var cancel context.CancelFunc
	ctx, cancel = context.WithCancel(context.Background())
	defer cancel()

	db = initDB()

	rdb = redis.NewClient(&redis.Options{
		Addr: "localhost:6379",
	})

	defer rdb.Close()

	if err := rdb.Ping(ctx).Err(); err != nil {
		log.Fatalf("redis connection failed:%v", err)
	}

	go startExpressWorker(ctx, db, rdb)
	go startNormalWorker(ctx, db, rdb)

	r := gin.Default()
	r.POST("/api/messages/bulk/send", sendBulkMessagesHandler)
	r.POST("/api/credit/increase", increaseCreditHandler)
	r.GET("/api/messages/sent-log", messagesSentLogHandler)
	r.Run(":7075")

	///todo for close db and redis
}

type request struct {
	Messages []messageDto `json:"messages"`
}
type messageDto struct {
	ToNumber   string `json:"to"`
	FromNumber string `json:"from"`
	Content    string `json:"content"`
}

func messagesSentLogHandler(c *gin.Context) {
	clientID := c.GetHeader("X-Client-ID")
	if clientID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "missing client id"})
		return
	}

	status := c.Query("status")
	msgType := c.Query("type")
	fromDate := c.Query("from_date")
	toDate := c.Query("to_date")

	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	pageSize, _ := strconv.Atoi(c.DefaultQuery("page_size", "20"))
	offset := (page - 1) * pageSize

	query := db.Model(&models.Message{}).Where("client_id = ?", clientID)
	if status != "" {
		query = query.Where("status = ?", status)
	}
	if msgType != "" {
		query = query.Where("type = ?", msgType)
	}
	if fromDate != "" {
		query = query.Where("queued_at >= ?", fromDate)
	}
	if toDate != "" {
		query = query.Where("queued_at <= ?", toDate)
	}

	var total int64
	query.Count(&total)
	var messages []models.Message
	if err := query.Order("queued_at desc").Limit(pageSize).Offset(offset).Find(&messages).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"page":       page,
		"page_size":  pageSize,
		"total":      total,
		"total_pages": (total + int64(pageSize) - 1) / int64(pageSize),
		"messages":   messages,
	})
}

type creditDto struct {
	Name    string `json:"name"`
	Balance int    `json:"balance"`
}

func increaseCreditHandler(c *gin.Context) {
	clientID := c.GetHeader("X-Client-ID")
	var creditDto creditDto
	if err := c.ShouldBindJSON(&creditDto); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	var credit models.Credit
	err := db.First(&credit, "client_id=?", clientID).Error
	if err != nil {
		credit = models.Credit{
			ClientID:  clientID,
			Balance:   creditDto.Balance,
			Name:      creditDto.Name,
			UpdatedAt: time.Now(),
		}
		db.Save(credit)
	} else {
		credit.Balance = creditDto.Balance
		credit.UpdatedAt = time.Now()
		db.Save(credit)
	}

	key := fmt.Sprintf("balance:%s", clientID)
	err = rdb.Set(ctx, key, credit.Balance, 0).Err()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"status": "credit increased"})
}

func sendBulkMessagesHandler(c *gin.Context) {
	clientID := c.GetHeader("X-Client-ID")
	msgType := c.GetHeader("X-SMS-TYPE")

	var req request
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	key := fmt.Sprintf("balance:%s", clientID)
	lm := len(req.Messages)
	used, err := decCreditScripr.Eval(ctx, rdb, []string{key}, strconv.Itoa(lm)).Int()
	if err != nil {
		if strings.Contains(err.Error(), "no_balance") {
			c.JSON(http.StatusPaymentRequired, gin.H{"error": "no balance"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	if used == 0 {
		c.JSON(http.StatusPaymentRequired, gin.H{"error": "no credit left"})
		return
	}

	accepted := req.Messages[:used]
	queueKey := fmt.Sprintf("sms:%s", msgType)
	go func() {

		for _, m := range accepted {
			id := uuid.New()
			payload := fmt.Sprintf("%s|%s|%s|%s|%s", id.String(), clientID, m.ToNumber, m.FromNumber, m.Content)
			if err := rdb.RPush(c, queueKey, payload).Err(); err != nil {
				log.Printf("Queue error:", err)
				refundResult, err := refundScript.Run(ctx, rdb, []string{key}, 1).Result()
				if err != nil {
					log.Println("refund error:", err)
				}
				log.Println("refund:", refundResult)
			}

		}

	}()
	c.JSON(http.StatusOK, gin.H{
		"accepted": used,
		"rejected": len(req.Messages) - used,
		"message":  "messages queued successfully",
	})

}

func startExpressWorker(ctx context.Context, db *gorm.DB, rdb *redis.Client) {
	log.Println("[express] worker started")
	for {
		res, err := rdb.BLPop(ctx, 0*time.Second, "sms:express").Result()
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			log.Println("[express] redis error:", err)
			time.Sleep(time.Second)
			continue
		}
		payload := res[1]
		processMessage(ctx, db, rdb, payload, configs.WorkerConfig{WorkerType: "express"})
	}
}

func startNormalWorker(ctx context.Context, db *gorm.DB, rdb *redis.Client) {
	log.Println("[normal] worker started")
	for {
		res, err := rdb.BLPop(ctx, 0*time.Second, "sms:normal").Result()
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			log.Println("[normal] redis error:", err)
			time.Sleep(time.Second)
			continue
		}
		payload := res[1]
		processMessage(ctx, db, rdb, payload, configs.WorkerConfig{WorkerType: "normal"})
	}
}

func insertMessage(db *gorm.DB, message models.Message, key string) error {
	return db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(&message).Error; err != nil {
			return err
		}
		var credit models.Credit
		if err := tx.First(&credit, "client_id = ?", message.ClientID).Error; err != nil {
			return err
		}
		if credit.Balance <= 0 {
			return errors.New("insuffficient balance credit")
		}

		credit.Balance -= 1
		credit.UpdatedAt = time.Now()
		if err := tx.Save(&credit).Error; err != nil {
			return err
		}
		return nil
	})
}
func updateMessage(db *gorm.DB, id string, status string, errMessage *string) error {
	return db.Transaction(func(tx *gorm.DB) error {
		var message models.Message
		if err := tx.First(&message, "id=?", id).Error; err != nil {
			log.Println("error of message with id ", err, id)
			return err
		}
		message.Status = status
		message.UpdatedAt = time.Now()
		message.ErrorMessage = errMessage
		if err := tx.Save(&message).Error; err != nil {
			return err
		}
		return nil
	})
}

func processMessage(ctx context.Context, db *gorm.DB, rdb *redis.Client, payload string, cfg configs.WorkerConfig) error {
	parts := strings.SplitN(payload, "|", 5)
	if len(parts) < 5 {
		log.Printf("[%s] invalid payload format: %s", cfg.WorkerType, payload)
		return fmt.Errorf("[%s] invalid payload format: %s", cfg.WorkerType, payload)
	}

	id := parts[0]
	clientID := parts[1]
	to := parts[2]
	from := parts[3]
	body := parts[4]

	success, errMsg := sendToProvider(ctx, to, from, body)

	status := "sent"
	if !success {
		status = "failed"
	}

	message := models.Message{
		ID:           id,
		ClientID:     clientID,
		Type:         cfg.WorkerType,
		ToNumber:     to,
		FromNumber:   from,
		Content:      body,
		Status:       status,
		ErrorMessage: &errMsg,
		QueuedAt:     time.Now(),
	}
	log.Println("id", message.ID)
	key := "balance:" + clientID
	err := insertMessage(db, message, key)
	if err != nil {
		refundResult, err := refundScript.Run(ctx, rdb, []string{}, 1).Result()
		if err != nil {
			log.Println("refund error:", err)
		}
		log.Println("refund:", refundResult)
	}

	if !success {
		_, err := rdb.Eval(ctx, `
            if redis.call("EXISTS", KEYS[1]) == 1 then
                redis.call("INCRBY", KEYS[1], 1)
            else
                redis.call("SET", KEYS[1], 1)
            end
            return redis.call("GET", KEYS[1])
        `, []string{key}).Result()
		if err != nil {
			log.Printf("[%s] refund error for client %s: %v", cfg.WorkerType, clientID, err)
		}
	}
	return nil
}

func sendToProvider(ctx context.Context, to string, from string, content string) (bool, string) {
	log.Print("send")
	return true, ""
}
