package store

import (
	"net/http"

	"github.com/jackc/pgx/v5/pgxpool"
)

type Store struct{ pool *pgxpool.Pool }

func Open() *Store { return &Store{} }

func (s *Store) Orders(w http.ResponseWriter, r *http.Request) {}
