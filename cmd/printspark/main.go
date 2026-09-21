package main

import (
	"fmt"
	"path/filepath"
	"github.com/Neon-Dolls/neondoll/DollCard"
)

func main() {
	state, err := dollcard.Decode(filepath.Join("testdata", "dolls", "spark.dollcard"))
	if err != nil {
		panic(err)
	}
	fmt.Printf("Name: %s\n", state.Identity.CanonicalName)
	fmt.Printf("Doll ID: %s\n", state.Identity.DollID)
	fmt.Printf("Soul: %s\n", state.Soul.Content)
	fmt.Printf("Drives: %+v\n", state.Drives)
	fmt.Printf("Goals: %+v\n", state.Goals)
	fmt.Printf("Intentions: %+v\n", state.Intentions)
	fmt.Printf("Memories: %d items\n", len(state.Memories.Items))
	fmt.Printf("Version: %d\n", state.Version)
}