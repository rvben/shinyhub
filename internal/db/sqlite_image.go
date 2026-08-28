package db

import (
	"context"
	"errors"
	"fmt"
)

// sqliteSerializer and sqliteDeserializer are the parts of modernc's driver
// connection that expose sqlite3_serialize / sqlite3_deserialize. Asserting an
// interface keeps the driver type out of this package's API.
type sqliteSerializer interface {
	Serialize() ([]byte, error)
}

type sqliteDeserializer interface {
	Deserialize([]byte) error
}

// SerializeSQLite returns the SQLite database as the byte image that
// sqlite3_serialize produces: for an in-memory database, exactly the bytes a
// backup to disk would write. Cloning a migrated schema from an image is far
// cheaper than replaying the migrations, which is what test fixtures use it
// for. It fails on a Postgres store.
func (s *Store) SerializeSQLite(ctx context.Context) ([]byte, error) {
	if s.IsPostgres() {
		return nil, errors.New("serialize sqlite: store is not SQLite")
	}
	conn, err := s.rawDB().Conn(ctx)
	if err != nil {
		return nil, fmt.Errorf("serialize sqlite: acquire conn: %w", err)
	}
	defer conn.Close()

	var image []byte
	err = conn.Raw(func(dc any) error {
		ser, ok := dc.(sqliteSerializer)
		if !ok {
			return fmt.Errorf("driver conn %T cannot serialize", dc)
		}
		var serr error
		image, serr = ser.Serialize()
		return serr
	})
	if err != nil {
		return nil, fmt.Errorf("serialize sqlite: %w", err)
	}
	if len(image) == 0 {
		return nil, errors.New("serialize sqlite: driver returned an empty image")
	}
	return image, nil
}

// DeserializeSQLite replaces the store's database with image, a value
// SerializeSQLite returned, including its schema_migrations ledger. Only an
// in-memory store may be deserialized: it holds exactly one connection, so the
// replaced database is the whole store. A file-backed pool would replace the
// database on one connection only, so that is refused rather than half-done.
// Connection-level pragmas (foreign_keys, busy_timeout) are not part of the
// image and stay as Open configured them.
func (s *Store) DeserializeSQLite(ctx context.Context, image []byte) error {
	if s.IsPostgres() {
		return errors.New("deserialize sqlite: store is not SQLite")
	}
	if !s.memory {
		return errors.New("deserialize sqlite: only an in-memory store can be deserialized")
	}
	if len(image) == 0 {
		return errors.New("deserialize sqlite: empty image")
	}
	conn, err := s.rawDB().Conn(ctx)
	if err != nil {
		return fmt.Errorf("deserialize sqlite: acquire conn: %w", err)
	}
	defer conn.Close()

	err = conn.Raw(func(dc any) error {
		des, ok := dc.(sqliteDeserializer)
		if !ok {
			return fmt.Errorf("driver conn %T cannot deserialize", dc)
		}
		return des.Deserialize(image)
	})
	if err != nil {
		return fmt.Errorf("deserialize sqlite: %w", err)
	}
	return nil
}
