package main

import (
	"flag"
	"fmt"
	"log"

	"github.com/CHESSComputing/gonexus/gonexus"
)

func main() {
	var fin string
	flag.StringVar(&fin, "fin", "", "nexus file")
	flag.Parse()
	root, err := gonexus.Load(fin)
	if err != nil {
		log.Fatal(err)
	}

	// Print the whole tree, like Python's `print(root.tree)`.
	fmt.Println(root.Tree())

	// Access by NeXus class, like Python's root.NXentry[0].
	for _, entry := range root.Component("NXentry") {
		fmt.Println("entry:", entry.NXName())
	}
}
