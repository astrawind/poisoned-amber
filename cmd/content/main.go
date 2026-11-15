package main

import (
	"flag"
	"fmt"
	"os"
	db "poisoned-amber/internal/adapters/sqlite"
	"poisoned-amber/internal/domain"
)

func main() {
	file := flag.String("file", "content/test.json", "content package")
	database := flag.String("db", "", "import into SQLite (optional)")
	flag.Parse()
	if e := run(*file, *database); e != nil {
		fmt.Fprintln(os.Stderr, e)
		os.Exit(1)
	}
}
func run(file, database string) error {
	f, e := os.Open(file)
	if e != nil {
		return e
	}
	defer f.Close()
	c, e := domain.ReadContent(f)
	if e != nil {
		return e
	}
	if database != "" {
		s, e := db.Open(database)
		if e != nil {
			return e
		}
		defer s.Close()
		if e = s.Import(c); e != nil {
			return e
		}
	}
	fmt.Println("Valid content:", c.Version)
	return nil
}
