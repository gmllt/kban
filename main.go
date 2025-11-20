package main

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"time"

	"github.com/gorilla/mux"
	"gopkg.in/yaml.v3"
)

type S3Config struct {
	Endpoint        string `yaml:"endpoint"`
	Bucket          string `yaml:"bucket"`
	Region          string `yaml:"region"`
	AccessKey       string `yaml:"access_key"`
	SecretKey       string `yaml:"secret_key"`
	UsePathStyle    bool   `yaml:"use_path_style"`
	DisableChecksum bool   `yaml:"disable_checksum"`
}

type Config struct {
	S3 S3Config `yaml:"s3"`
}

func loadConfig(path string) (*Config, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var cfg Config
	dec := yaml.NewDecoder(f)
	if err := dec.Decode(&cfg); err != nil {
		return nil, err
	}
	return &cfg, nil
}

func main() {
	cfg, err := loadConfig("config.yml")
	if err != nil {
		log.Fatalf("Failed to load config: %v", err)
	}
	// Initialiser S3
	s3Client, err := NewS3Client(cfg.S3)
	if err != nil {
		log.Fatalf("Failed to init S3: %v", err)
	}

	r := mux.NewRouter()

	// API: Get board
	r.HandleFunc("/api/board", func(w http.ResponseWriter, r *http.Request) {
		log.Printf("[GET] /api/board")
		board, err := LoadBoard(s3Client, cfg.S3)
		if err != nil {
			log.Printf("Error loading board: %v", err)
			w.WriteHeader(500)
			w.Write([]byte("Failed to load board"))
			return
		}
		log.Printf("Board loaded: %d cards", len(board.Cards))
		json.NewEncoder(w).Encode(board)
	}).Methods("GET")

	// API: Create card
	r.HandleFunc("/api/card", func(w http.ResponseWriter, r *http.Request) {
		log.Printf("[POST] /api/card")
		var card Card
		if err := json.NewDecoder(r.Body).Decode(&card); err != nil {
			log.Printf("Error decoding card: %v", err)
			w.WriteHeader(400)
			return
		}
		log.Printf("Payload: %+v", card)
		board, err := LoadBoard(s3Client, cfg.S3)
		if err != nil {
			log.Printf("Error loading board: %v", err)
			w.WriteHeader(500)
			return
		}
		card.ID = generateID()
		// Position = dernier dans la colonne
		maxPos := -1
		for _, c := range board.Cards {
			if c.Status == card.Status && c.Position > maxPos {
				maxPos = c.Position
			}
		}
		card.Position = maxPos + 1
		board.Cards = append(board.Cards, card)
		if err := SaveBoard(s3Client, cfg.S3, board); err != nil {
			log.Printf("Error saving board: %v", err)
			w.WriteHeader(500)
			return
		}
		log.Printf("Card created: %+v", card)
		json.NewEncoder(w).Encode(card)
	}).Methods("POST")

	// API: Update card (inclut position)
	r.HandleFunc("/api/card/{id}", func(w http.ResponseWriter, r *http.Request) {
		vars := mux.Vars(r)
		id := vars["id"]
		log.Printf("[PUT] /api/card/%s", id)
		var update Card
		if err := json.NewDecoder(r.Body).Decode(&update); err != nil {
			log.Printf("Error decoding update: %v", err)
			w.WriteHeader(400)
			return
		}
		log.Printf("Payload: %+v", update)
		board, err := LoadBoard(s3Client, cfg.S3)
		if err != nil {
			log.Printf("Error loading board: %v", err)
			w.WriteHeader(500)
			return
		}
		var oldStatus string
		var oldPosition int
		updated := false
		for i, c := range board.Cards {
			if c.ID == id {
				oldStatus = c.Status
				oldPosition = c.Position
				board.Cards[i].Title = update.Title
				board.Cards[i].Description = update.Description
				board.Cards[i].Status = update.Status
				if update.Position != 0 || update.Position == 0 {
					board.Cards[i].Position = update.Position
				}
				updated = true
				break
			}
		}
		if !updated {
			log.Printf("Card not found: %s", id)
			w.WriteHeader(404)
			return
		}
		// Réordonner les positions dans la colonne si la position a changé
		if oldStatus != update.Status || oldPosition != update.Position {
			// Find index of moving card in board
			moveIdx := findCardIndex(board.Cards, id)
			if moveIdx == -1 {
				log.Printf("Moving card not found in board: %s", id)
			} else {
				// Build list of other cards in the target column (exclude moving card)
				others := []Card{}
				for _, cc := range board.Cards {
					if cc.ID == id {
						continue
					}
					if cc.Status == update.Status {
						others = append(others, cc)
					}
				}
				// Clamp requested position
				pos := update.Position
				if pos < 0 {
					pos = 0
				}
				if pos > len(others) {
					pos = len(others)
				}
				// Build new order for the column: insert moving card at pos
				movingCard := board.Cards[moveIdx]
				movingCard.Status = update.Status
				newOrder := make([]Card, 0, len(others)+1)
				newOrder = append(newOrder, others[:pos]...)
				newOrder = append(newOrder, movingCard)
				if pos < len(others) {
					newOrder = append(newOrder, others[pos:]...)
				}
				// Reassign positions for cards in this column
				for i, nc := range newOrder {
					for j := range board.Cards {
						if board.Cards[j].ID == nc.ID {
							board.Cards[j].Position = i
							board.Cards[j].Status = update.Status
						}
					}
				}
			}
		}
		if err := SaveBoard(s3Client, cfg.S3, board); err != nil {
			log.Printf("Error saving board: %v", err)
			w.WriteHeader(500)
			return
		}
		log.Printf("Card updated: %s", id)
		w.WriteHeader(204)
	}).Methods("PUT")

	// API: Delete card
	r.HandleFunc("/api/card/{id}", func(w http.ResponseWriter, r *http.Request) {
		vars := mux.Vars(r)
		id := vars["id"]
		log.Printf("[DELETE] /api/card/%s", id)
		board, err := LoadBoard(s3Client, cfg.S3)
		if err != nil {
			log.Printf("Error loading board: %v", err)
			w.WriteHeader(500)
			return
		}
		countBefore := len(board.Cards)
		newCards := []Card{}
		for _, c := range board.Cards {
			if c.ID != id {
				newCards = append(newCards, c)
			}
		}
		if len(newCards) == countBefore {
			log.Printf("Card not found for delete: %s", id)
			w.WriteHeader(404)
			return
		}
		board.Cards = newCards
		if err := SaveBoard(s3Client, cfg.S3, board); err != nil {
			log.Printf("Error saving board: %v", err)
			w.WriteHeader(500)
			return
		}
		log.Printf("Card deleted: %s", id)
		w.WriteHeader(204)
	}).Methods("DELETE")

	// Servir les fichiers statiques
	r.PathPrefix("/").Handler(http.FileServer(http.Dir("static")))

	log.Println("Kanban server starting on :8080")
	http.ListenAndServe(":8080", r)
}

func generateID() string {
	// Génère un ID unique simple (à améliorer si besoin)
	return fmt.Sprintf("%d", time.Now().UnixNano())
}

func findCardIndex(cards []Card, id string) int {
	for i, c := range cards {
		if c.ID == id {
			return i
		}
	}
	return -1
}
