// Package outbox 实现事务 Outbox 的发布端（协议文档 §11.1）。
// 事件先提交再发布，允许至少一次投递；publisher 可重复发布，消费者必须幂等。
package outbox

import (
	"context"
	"database/sql"
	"log"
	"time"
)

// Publisher 轮询未发布的 outbox 消息并标记 published_at。
// M1/M2 中 SSE 直接从 stream_events replay，Outbox 为后续外部订阅
// （审计导出、集成通道）保留投递语义。
type Publisher struct {
	db       *sql.DB
	interval time.Duration
	// publish 是投递钩子；默认仅记录日志，M4 接外部通道。
	publish func(eventID, topic string, payload []byte)
}

func NewPublisher(db *sql.DB) *Publisher {
	return &Publisher{
		db: db, interval: 5 * time.Second,
		publish: func(eventID, topic string, payload []byte) {
			log.Printf("outbox: 发布 event=%s topic=%s (%d bytes)", eventID, topic, len(payload))
		},
	}
}

// Run 阻塞运行发布循环直到 ctx 取消。
func (p *Publisher) Run(ctx context.Context) {
	ticker := time.NewTicker(p.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := p.drain(ctx); err != nil {
				log.Printf("outbox: 发布失败: %v", err)
			}
		}
	}
}

func (p *Publisher) drain(ctx context.Context) error {
	rows, err := p.db.QueryContext(ctx,
		`SELECT id, event_id, topic, payload FROM outbox_messages
		 WHERE published_at IS NULL ORDER BY id LIMIT 100`)
	if err != nil {
		return err
	}
	type msg struct {
		id      int64
		eventID string
		topic   string
		payload []byte
	}
	var msgs []msg
	for rows.Next() {
		var m msg
		var payload []byte
		if err := rows.Scan(&m.id, &m.eventID, &m.topic, &payload); err != nil {
			rows.Close()
			return err
		}
		m.payload = payload
		msgs = append(msgs, m)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	for _, m := range msgs {
		p.publish(m.eventID, m.topic, m.payload)
		if _, err := p.db.ExecContext(ctx,
			`UPDATE outbox_messages SET published_at=? WHERE id=?`,
			time.Now().UTC(), m.id); err != nil {
			return err
		}
	}
	return nil
}

// Backlog 返回未发布消息数（健康观测用）。
func Backlog(ctx context.Context, db *sql.DB) (int, error) {
	var n int
	err := db.QueryRowContext(ctx, `SELECT count(*) FROM outbox_messages WHERE published_at IS NULL`).Scan(&n)
	return n, err
}
