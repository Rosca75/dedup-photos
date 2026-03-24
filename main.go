// =============================================================================
// main.go — Entry point for the DedupPhotos application
// =============================================================================
//
// This file is the starting point of the program. When you run the compiled
// binary, Go looks for the "main" function inside "package main" and executes
// it. Think of it like the "public static void main" in Java or the
// "if __name__ == '__main__'" block in Python.
//
// What this file does:
//   1. Defines command-line flags (--port, --help) so the user can customize
//      behaviour when launching the program from a terminal.
//   2. Prints a friendly ASCII-art banner so you know the server started.
//   3. Calls StartServer() (defined in server.go) to spin up the HTTP server.
// =============================================================================

package main

import (
	"flag" // "flag" is Go's built-in package for parsing command-line arguments.
	"fmt"  // "fmt" is Go's formatted I/O package — like printf in C.
	"os"   // "os" gives us access to operating-system features like exiting.
)

// main is the entry point of the application. Every Go program needs exactly
// one main() function inside package main. Go calls this automatically when
// the program starts.
func main() {
	// -------------------------------------------------------------------------
	// Step 1: Define command-line flags
	// -------------------------------------------------------------------------
	//
	// flag.Int creates a flag named "port" with a default value of 8080.
	// The third argument is the help text shown when you run --help.
	// The function returns a *int (a pointer to an int), which means "port"
	// holds the memory address where the actual integer value lives.
	// We dereference it later with *port to get the actual number.
	port := flag.Int("port", 8080, "Port number for the web server (default: 8080)")

	// flag.Bool creates a boolean flag. If the user passes --help, this
	// becomes true. We use this to print usage information and exit.
	help := flag.Bool("help", false, "Show this help message and exit")

	// flag.Parse() actually reads the command-line arguments (os.Args) and
	// fills in the values for all the flags we defined above. You MUST call
	// this before accessing any flag values — otherwise they'll all be
	// defaults regardless of what the user typed.
	flag.Parse()

	// -------------------------------------------------------------------------
	// Step 2: Handle --help flag
	// -------------------------------------------------------------------------
	//
	// If the user passed --help (or -help), we print usage info and exit.
	// The * in *help dereferences the pointer — it gets the actual bool value.
	if *help {
		// Print a short description of the program.
		fmt.Println("DedupPhotos — Find and manage duplicate photos on your computer.")
		fmt.Println()
		fmt.Println("Usage:")
		fmt.Println("  dedup-photos [--port PORT]")
		fmt.Println()
		fmt.Println("Flags:")

		// flag.PrintDefaults() prints all registered flags with their default
		// values and help text. This is a convenience from the flag package.
		flag.PrintDefaults()

		// os.Exit(0) terminates the program immediately with exit code 0,
		// which means "success" by convention. We exit here because --help
		// should only print info, not start the server.
		os.Exit(0)
	}

	// -------------------------------------------------------------------------
	// Step 3: Print the ASCII art banner
	// -------------------------------------------------------------------------
	//
	// This is purely cosmetic — it prints a nice logo in the terminal so the
	// user knows the server has started and where to find it. The backtick (`)
	// creates a "raw string literal" in Go, which can span multiple lines and
	// doesn't process escape characters like \n.
	banner := `
 ____           _             ____  _           _
|  _ \  ___  __| |_   _ _ __ |  _ \| |__   ___ | |_ ___  ___
| | | |/ _ \/ _' | | | | '_ \| |_) | '_ \ / _ \| __/ _ \/ __|
| |_| |  __/ (_| | |_| | |_) |  __/| | | | (_) | || (_) \__ \
|____/ \___|\__,_|\__,_| .__/|_|   |_| |_|\___/ \__\___/|___/
                       |_|
`
	// fmt.Println prints the banner string followed by a newline.
	fmt.Println(banner)

	// fmt.Printf is like printf in C — it uses format verbs like %d (integer)
	// to insert values into the string. \n is a newline character.
	// *port dereferences the pointer to get the actual integer value.
	fmt.Printf("  🌐 Server starting at: http://localhost:%d\n", *port)
	fmt.Println("  Press Ctrl+C to stop the server.")
	fmt.Println()

	// -------------------------------------------------------------------------
	// Step 4: Start the HTTP server
	// -------------------------------------------------------------------------
	//
	// StartServer is defined in server.go. It sets up all the HTTP routes
	// (like GET / and POST /api/scan) and begins listening for connections.
	// This function blocks (never returns) because the server runs forever
	// until you kill the process.
	StartServer(*port)
}
