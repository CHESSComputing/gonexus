// Command example demonstrates basic gonexus usage: building a NeXus tree
// in memory, saving it to disk, reloading it, and printing/traversing it.
//
// Run with:
//
//	go run ./example
package main

import (
	"fmt"
	"log"
	"math"

	"github.com/CHESSComputing/gonexus/gonexus"
)

func main() {
	// --- Build some sample data -------------------------------------
	const n = 101
	x := make([]float64, n)
	y := make([]float64, n)
	z := make([]float64, n*n)
	for i := 0; i < n; i++ {
		x[i] = 2 * math.Pi * float64(i) / (n - 1)
		y[i] = x[i]
	}
	for i := 0; i < n; i++ {
		for j := 0; j < n; j++ {
			z[i*n+j] = math.Sin(x[j]) * math.Sin(y[i])
		}
	}
	zField := gonexus.NewField(z, "z")
	zField.Shape = []int{n, n}
	xField := gonexus.NewField(x, "x")
	yField := gonexus.NewField(y, "y")

	// --- Assemble a NeXus tree ----------------------------------------
	data := gonexus.NewNXdata(zField, xField, yField)

	sample := gonexus.NewNXsample()
	sample.InsertField("temperature", 40.0).SetUnits("K")
	sample.InsertField("name", "demo sample")

	entry := gonexus.NewNXentry()
	entry.Insert("data", data)
	entry.Insert("sample", sample)

	root := gonexus.NewNXroot(entry)

	// --- Save it --------------------------------------------------------
	if err := gonexus.Save("example.nxs", root); err != nil {
		log.Fatalf("save: %v", err)
	}
	fmt.Println("wrote example.nxs")

	// --- Reload and inspect ---------------------------------------------
	loaded, err := gonexus.Load("example.nxs")
	if err != nil {
		log.Fatalf("load: %v", err)
	}
	fmt.Println(loaded.Tree())

	temp, err := loaded.GetField("entry/sample/temperature")
	if err != nil {
		log.Fatalf("get field: %v", err)
	}
	fmt.Printf("sample temperature: %v %s\n", temp.Scalar(), temp.Units())

	for _, dataGroup := range loaded.Component("NXdata") {
		fmt.Printf("found NXdata group %q with signal %q\n",
			dataGroup.NXPath(), dataGroup.Signal().NXName())
	}
}
