package store

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

type Recording struct {
	ID             int64  `json:"id"`
	LessonID       int64  `json:"lesson_id"`
	Discipline     string `json:"discipline"`
	Date           string `json:"date"`
	LectureNum     int    `json:"lecture_num"`
	VideoPath      string `json:"video_path"`
	AudioPath      string `json:"audio_path"`
	TelegramFileID string `json:"telegram_file_id"`
	TelegramMsgID  int64  `json:"telegram_msg_id"`
	TranscriptText string `json:"transcript_text"`
	SummaryText    string `json:"summary_text"`
	HasTestAlert   bool   `json:"has_test_alert"`
	CreatedAt      string `json:"created_at"`
}

func (s *Store) SaveRecording(ctx context.Context, r *Recording) error {
	if r == nil {
		return fmt.Errorf("recording is nil")
	}
	if r.CreatedAt == "" {
		r.CreatedAt = time.Now().UTC().Format(time.RFC3339)
	}

	query := `INSERT INTO recordings (
		lesson_id, discipline, date, lecture_num, video_path, audio_path,
		telegram_file_id, telegram_msg_id, transcript_text, summary_text, has_test_alert, created_at
	) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`

	hasTestInt := 0
	if r.HasTestAlert {
		hasTestInt = 1
	}

	res, err := s.db.ExecContext(ctx, query,
		r.LessonID, r.Discipline, r.Date, r.LectureNum, r.VideoPath, r.AudioPath,
		r.TelegramFileID, r.TelegramMsgID, r.TranscriptText, r.SummaryText, hasTestInt, r.CreatedAt,
	)
	if err != nil {
		return fmt.Errorf("store: save recording: %w", err)
	}

	id, err := res.LastInsertId()
	if err == nil {
		r.ID = id
	}
	return nil
}

func (s *Store) GetDisciplines(ctx context.Context) ([]string, error) {
	query := `SELECT DISTINCT discipline FROM recordings WHERE discipline != '' ORDER BY discipline ASC`
	rows, err := s.db.QueryContext(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("store: get disciplines: %w", err)
	}
	defer rows.Close()

	var list []string
	for rows.Next() {
		var d string
		if err := rows.Scan(&d); err == nil && d != "" {
			list = append(list, d)
		}
	}
	return list, nil
}

func (s *Store) GetRecordingsByDiscipline(ctx context.Context, discipline string) ([]Recording, error) {
	query := `SELECT id, lesson_id, discipline, date, lecture_num, video_path, audio_path,
		telegram_file_id, telegram_msg_id, transcript_text, summary_text, has_test_alert, created_at
		FROM recordings WHERE discipline = ? ORDER BY lecture_num ASC, id ASC`

	rows, err := s.db.QueryContext(ctx, query, discipline)
	if err != nil {
		return nil, fmt.Errorf("store: get recordings by discipline: %w", err)
	}
	defer rows.Close()

	var list []Recording
	for rows.Next() {
		var r Recording
		var hasTestInt int
		if err := rows.Scan(
			&r.ID, &r.LessonID, &r.Discipline, &r.Date, &r.LectureNum, &r.VideoPath, &r.AudioPath,
			&r.TelegramFileID, &r.TelegramMsgID, &r.TranscriptText, &r.SummaryText, &hasTestInt, &r.CreatedAt,
		); err != nil {
			return nil, err
		}
		r.HasTestAlert = (hasTestInt == 1)
		list = append(list, r)
	}
	return list, nil
}

func (s *Store) GetRecordingByID(ctx context.Context, id int64) (*Recording, error) {
	query := `SELECT id, lesson_id, discipline, date, lecture_num, video_path, audio_path,
		telegram_file_id, telegram_msg_id, transcript_text, summary_text, has_test_alert, created_at
		FROM recordings WHERE id = ?`

	row := s.db.QueryRowContext(ctx, query, id)
	var r Recording
	var hasTestInt int
	if err := row.Scan(
		&r.ID, &r.LessonID, &r.Discipline, &r.Date, &r.LectureNum, &r.VideoPath, &r.AudioPath,
		&r.TelegramFileID, &r.TelegramMsgID, &r.TranscriptText, &r.SummaryText, &hasTestInt, &r.CreatedAt,
	); err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, err
	}
	r.HasTestAlert = (hasTestInt == 1)
	return &r, nil
}
