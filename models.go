package main

type Card struct {
	ID          string `json:"id"`
	Title       string `json:"title"`
	Description string `json:"description"`
	Status      string `json:"status"` // ToDo, Doing, Hold, Done
	Position    int    `json:"position"`
}

type Board struct {
	Cards []Card `json:"cards"`
}
