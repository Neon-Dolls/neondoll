package persistence

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/Neon-Dolls/neondoll/Core/SessionEvent"
)

var _ sessionevent.Store = (*store)(nil)

const deliveryColumns = `delivery_id, event_id, session_id, target_principal_id, owner_kind, owner_instance_id,
	event_json, status, error, created_at, updated_at`

func (s *store) CreateDelivery(ctx context.Context, delivery *sessionevent.Delivery) error {
	eventJSON, err := json.Marshal(delivery.Event)
	if err != nil {
		return fmt.Errorf("encode session event: %w", err)
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO interaction_session_event_deliveries (`+deliveryColumns+`)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, delivery.ID, delivery.Event.ID, delivery.Event.SessionID,
		delivery.TargetPrincipalID, delivery.Owner.Kind, delivery.Owner.InstanceID, string(eventJSON), delivery.Status,
		delivery.Error, formatTime(delivery.CreatedAt), formatTime(delivery.UpdatedAt))
	if err != nil {
		return fmt.Errorf("create session event delivery: %w", err)
	}
	return nil
}

func (s *store) UpdateDelivery(ctx context.Context, id string, status sessionevent.DeliveryStatus, failure string, at time.Time) (*sessionevent.Delivery, error) {
	result, err := s.db.ExecContext(ctx, `UPDATE interaction_session_event_deliveries SET status = ?, error = ?, updated_at = ? WHERE delivery_id = ?`, status, failure, formatTime(at), id)
	if err != nil {
		return nil, fmt.Errorf("update session event delivery: %w", err)
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return nil, err
	}
	if rows != 1 {
		return nil, sessionevent.ErrInvalidEvent
	}
	return s.LoadDelivery(ctx, id)
}

func (s *store) LoadDelivery(ctx context.Context, id string) (*sessionevent.Delivery, error) {
	return scanDelivery(s.db.QueryRowContext(ctx, `SELECT `+deliveryColumns+` FROM interaction_session_event_deliveries WHERE delivery_id = ?`, id))
}

func (s *store) ListPendingDeliveries(ctx context.Context) ([]*sessionevent.Delivery, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+deliveryColumns+` FROM interaction_session_event_deliveries WHERE status = ? ORDER BY created_at, delivery_id`, sessionevent.DeliveryPending)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []*sessionevent.Delivery
	for rows.Next() {
		delivery, err := scanDelivery(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, delivery)
	}
	return result, rows.Err()
}

func scanDelivery(row rowScanner) (*sessionevent.Delivery, error) {
	var delivery sessionevent.Delivery
	var eventID, sessionID, eventJSON, createdAt, updatedAt string
	err := row.Scan(&delivery.ID, &eventID, &sessionID, &delivery.TargetPrincipalID, &delivery.Owner.Kind,
		&delivery.Owner.InstanceID, &eventJSON, &delivery.Status, &delivery.Error, &createdAt, &updatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, sessionevent.ErrInvalidEvent
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal([]byte(eventJSON), &delivery.Event); err != nil {
		return nil, fmt.Errorf("decode session event: %w", err)
	}
	if delivery.Event.ID != eventID || string(delivery.Event.SessionID) != sessionID {
		return nil, sessionevent.ErrInvalidEvent
	}
	delivery.CreatedAt, err = time.Parse(time.RFC3339Nano, createdAt)
	if err != nil {
		return nil, err
	}
	delivery.UpdatedAt, err = time.Parse(time.RFC3339Nano, updatedAt)
	if err != nil {
		return nil, err
	}
	return &delivery, nil
}
