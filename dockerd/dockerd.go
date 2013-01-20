package main

import (
	"errors"
	"log"
	"io"
	"net/http"
	"net/url"
	"os/exec"
	"flag"
	"reflect"
	"fmt"
	"github.com/kr/pty"
	"path"
	"strings"
	"time"
	"math/rand"
	"crypto/sha256"
	"bytes"
	"text/tabwriter"
)

func (docker *Docker) CmdHelp(stdin io.ReadCloser, stdout io.Writer, args ...string) error {
	log.Printf("Help %s\n", args)
	if len(args) == 0 {
		fmt.Fprintf(stdout, "Usage: docker COMMAND [arg...]\n\nA self-sufficient runtime for linux containers.\n\nCommands:\n")
		for _, cmd := range [][]interface{}{
			{"run", "Run a command in a container"},
			{"clone", "Duplicate a container"},
			{"list", "Display a list of containers"},
			{"layers", "Display a list of layers"},
			{"get", "Download a layer from a remote location"},
			{"wait", "Wait for the state of a container to change"},
			{"stop", "Stop a running container"},
			{"logs", "Fetch the logs of a container"},
			{"export", "Extract changes to a container's filesystem into a new layer"},
			{"attach", "Attach to the standard inputs and outputs of a running container"},
			{"info", "Display system-wide information"},
		} {
			fmt.Fprintf(stdout, "    %-10.10s%s\n", cmd...)
		}
	} else {
		if method := docker.getMethod(args[0]); method == nil {
			return errors.New("No such command: " + args[0])
		} else {
			method(stdin, stdout, "--help")
		}
	}
	return nil
}

func (docker *Docker) CmdLayers(stdin io.ReadCloser, stdout io.Writer, args ...string) error {
	flags := Subcmd(stdout, "layers", "[OPTIONS] [NAME]", "Show available filesystem layers")
	quiet := flags.Bool("q", false, "Quiet mode")
	flags.Parse(args)
	if flags.NArg() > 1 {
		flags.Usage()
		return nil
	}
	var nameFilter string
	if flags.NArg() == 1 {
		nameFilter = flags.Arg(0)
	}
	if *quiet {
		for id, layer := range docker.layers {
			if nameFilter != "" && nameFilter != layer.Name {
				continue
			}
			stdout.Write([]byte(id+ "\n"))
		}
	} else {
		w := tabwriter.NewWriter(stdout, 20, 1, 3, ' ', 0)
		fmt.Fprintf(w, "ID\tNAME\tSIZE\tADDED\tSOURCE\n")
		for _, layer := range docker.layers {
			if nameFilter != "" && nameFilter != layer.Name {
				continue
			}
			fmt.Fprintf(w, "%s\t%s\t%.1fM\t%s ago\t%s\n", layer.Id, layer.Name, float32(layer.Size) / 1024 / 1024, humanDuration(time.Now().Sub(layer.Added)), layer.Source)
		}
		w.Flush()
	}
	return nil
}

func (docker *Docker) CmdGet(stdin io.ReadCloser, stdout io.Writer, args ...string) error {
	if len(args) < 1 {
		return errors.New("Not enough arguments")
	}
	fmt.Fprintf(stdout, "Downloading from %s...\n", args[0])
	time.Sleep(2 * time.Second)
	layer := docker.addLayer(args[0], "download", 0)
	fmt.Fprintf(stdout, "New layer: %s %s %.1fM\n", layer.Id, layer.Name, float32(layer.Size) / 1024 / 1024)
	return nil
}

func (docker *Docker) CmdPut(stdin io.ReadCloser, stdout io.Writer, args ...string) error {
	if len(args) < 1 {
		return errors.New("Not enough arguments")
	}
	time.Sleep(1 * time.Second)
	layer := docker.addLayer(args[0], "upload", 0)
	fmt.Fprintf(stdout, "New layer: %s %s %.1fM\n", layer.Id, layer.Name, float32(layer.Size) / 1024 / 1024)
	return nil
}

func (docker *Docker) CmdExport(stdin io.ReadCloser, stdout io.Writer, args ...string) error {
	flags := Subcmd(stdout,
		"export", "CONTAINER LAYER",
		"Create a new layer from the changes on a container's filesystem")
	_ = flags.Bool("s", false, "Stream the new layer to the client intead of storing it on the docker")
	if err := flags.Parse(args); err != nil {
		return nil
	}
	if flags.NArg() < 2 {
		return errors.New("Not enough arguments")
	}
	if container, exists := docker.containers[flags.Arg(0)]; !exists {
		return errors.New("No such container")
	} else {
		// Extract actual changes here
		layer := docker.addLayer(flags.Arg(1), "export:" + container.Id, container.BytesChanged)
		fmt.Fprintf(stdout, "New layer: %s %s %.1fM\n", layer.Id, layer.Name, float32(layer.Size) / 1024 / 1024)
	}
	return nil
}


func (docker *Docker) addLayer(name string, source string, size uint) Layer {
	if size == 0 {
		size = uint(rand.Int31n(142 * 1024 * 1024))
	}
	layer := Layer{Id: randomId(), Name: name, Source: source, Added: time.Now(), Size: size}
	docker.layers[layer.Id] = layer
	return layer
}

type ArgList []string

func (l *ArgList) Set(value string) error {
	*l = append(*l, value)
	return nil
}

func (l *ArgList) String() string {
	return strings.Join(*l, ",")
}

func (docker *Docker) CmdRun(stdin io.ReadCloser, stdout io.Writer, args ...string) error {
	flags := Subcmd(stdout, "run", "-l LAYER [-l LAYER...] COMMAND {ARG...]", "Run a command in a container")
	fl_layers := new(ArgList)
	flags.Var(fl_layers, "l", "Add a layer to the filesystem. Multiple layers are added in the order they are defined")
	if err := flags.Parse(args); err != nil {
		return nil
	}
	if len(*fl_layers) < 1 {
		return errors.New("Please specify at least one layer")
	}
	if flags.NArg() < 1 {
		return errors.New("No command specified")
	}
	cmd := flags.Arg(0)
	var cmd_args []string
	if flags.NArg() > 1 {
		cmd_args = flags.Args()[1:]
	}
	container := Container{
		Id:	randomId(),
		Cmd:	cmd,
		Args:	cmd_args,
		Created: time.Now(),
		FilesChanged: uint(rand.Int31n(42)),
		BytesChanged: uint(rand.Int31n(24 * 1024 * 1024)),
	}
	for _, name := range *fl_layers {
		if layer, exists := docker.layers[name]; !exists {
			if srcContainer, exists := docker.containers[name]; exists {
				for _, layer := range srcContainer.Layers {
					container.Layers = append(container.Layers, layer)
				}
			} else {
				return errors.New("No such layer or container: " + name)
			}
		} else {
			container.Layers = append(container.Layers, layer)
		}
	}
	docker.containers[container.Id] = container
	return container.Run(stdin, stdout)
}

func (docker *Docker) CmdClone(stdin io.ReadCloser, stdout io.Writer, args ...string) error {
	flags := Subcmd(stdout, "Clone", "[OPTIONS] CONTAINER_ID", "Duplicate a container")
	reset := flags.Bool("r", true, "Reset: don't keep filesystem changes from the source container")
	flags.Parse(args)
	if !*reset {
		return errors.New("Only reset mode is available for now. Please use -r")
	}
	if flags.NArg() != 1 {
		flags.Usage()
		return nil
	}
	container, exists := docker.containers[flags.Arg(0)];
	if !exists {
		return errors.New("No such container: " + flags.Arg(0))
	}
	return docker.CmdRun(stdin, stdout, append([]string{"-l", container.Id, "--", container.Cmd}, container.Args...)...)
}

func startCommand(cmd *exec.Cmd, interactive bool) (io.WriteCloser, io.ReadCloser, error) {
	if interactive {
		term, err := pty.Start(cmd)
		if err != nil {
			return nil, nil, err
		}
		return term, term, nil
	}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, nil, err
	}
	return stdin, stdout, nil
}

func (docker *Docker) CmdList(stdin io.ReadCloser, stdout io.Writer, args ...string) error {
	var longestCol int
	for _, container := range docker.containers {
		if l := len(container.CmdString()); l > longestCol {
			longestCol = l
		}
	}
	if longestCol > 50 {
		longestCol = 50
	} else if longestCol < 5 {
		longestCol = 8
	}
	tpl := "%-16s   %-*.*s   %-6s   %-25s   %10s   %-s\n"
	fmt.Fprintf(stdout, tpl, "ID", longestCol, longestCol, "CMD", "STATUS", "CREATED", "CHANGES", "LAYERS")
	for _, container := range docker.containers {
		var layers []string
		for _, layer := range container.Layers {
			layers = append(layers, layer.Name)
		}
		fmt.Fprintf(stdout, tpl,
			/* ID */	container.Id,
			/* CMD */	longestCol, longestCol, container.CmdString(),
			/* STATUS */	"?",
			/* CREATED */	humanDuration(time.Now().Sub(container.Created)) + " ago",
			/* CHANGES */	fmt.Sprintf("%.1fM", float32(container.BytesChanged) / 1024 / 1024),
			/* LAYERS */	strings.Join(layers, ", "))
	}
	return nil
}

func main() {
	rand.Seed(time.Now().UTC().UnixNano())
	flag.Parse()
	if err := http.ListenAndServe(":4242", New()); err != nil {
		log.Fatal(err)
	}
}

func New() *Docker {
	return &Docker{
		layers: make(map[string]Layer),
		containers: make(map[string]Container),
	}
}

type AutoFlush struct {
	http.ResponseWriter
}

func (w *AutoFlush) Write(data []byte) (int, error) {
	ret, err := w.ResponseWriter.Write(data)
	if flusher, ok := w.ResponseWriter.(http.Flusher); ok {
		flusher.Flush()
	}
	return ret, err
}

func (docker *Docker) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	cmd, args := URLToCall(r.URL)
	log.Printf("%s\n", strings.Join(append(append([]string{"docker"}, cmd), args...), " "))
	if cmd == "" {
		docker.CmdUsage(r.Body, w, "")
		return
	}
	method := docker.getMethod(cmd)
	if method == nil {
		docker.CmdUsage(r.Body, w, cmd)
	} else {
		err := method(r.Body, &AutoFlush{w}, args...)
		if err != nil {
			fmt.Fprintf(w, "Error: %s\n", err)
		}
	}
}


func (docker *Docker) getMethod(name string) Cmd {
	methodName := "Cmd"+strings.ToUpper(name[:1])+strings.ToLower(name[1:])
	method, exists := reflect.TypeOf(docker).MethodByName(methodName)
	if !exists {
		return nil
	}
	return func(stdin io.ReadCloser, stdout io.Writer, args ...string) error {
		ret := method.Func.CallSlice([]reflect.Value{
			reflect.ValueOf(docker),
			reflect.ValueOf(stdin),
			reflect.ValueOf(stdout),
			reflect.ValueOf(args),
		})[0].Interface()
		if ret == nil {
			return nil
		}
		return ret.(error)
	}
}

func Go(f func() error) chan error {
	ch := make(chan error)
	go func() {
		ch <- f()
	}()
	return ch
}

type Docker struct {
	layers		map[string]Layer
	containers	map[string]Container
}

type Layer struct {
	Id	string
	Name	string
	Added	time.Time
	Size	uint
	Source	string
}

type Container struct {
	Id	string
	Cmd	string
	Args	[]string
	Layers	[]Layer
	Created	time.Time
	FilesChanged uint
	BytesChanged uint
	Running	bool
}

func (c *Container) Run(stdin io.ReadCloser, stdout io.Writer) error {
	// Not thread-safe
	if c.Running {
		return errors.New("Already running")
	}
	c.Running = true
	defer func() { c.Running = false }()
	cmd := exec.Command(c.Cmd, c.Args...)
	cmd_stdin, cmd_stdout, err := startCommand(cmd, false)
	if err != nil {
		return err
	}
	copy_out := Go(func() error {
		_, err := io.Copy(stdout, cmd_stdout)
		return err
	})
	copy_in := Go(func() error {
		//_, err := io.Copy(cmd_stdin, stdin)
		cmd_stdin.Close()
		stdin.Close()
		//return err
		return nil
	})
	if err := cmd.Wait(); err != nil {
		return err
	}
	if err := <-copy_in; err != nil {
		return err
	}
	if err := <-copy_out; err != nil {
		return err
	}
	return nil
}

func (c *Container) CmdString() string {
	return strings.Join(append([]string{c.Cmd}, c.Args...), " ")
}

type Cmd func(io.ReadCloser, io.Writer, ...string) error
type CmdMethod func(*Docker, io.ReadCloser, io.Writer, ...string) error

// Use this key to encode an RPC call into an URL,
// eg. domain.tld/path/to/method?q=get_user&q=gordon
const ARG_URL_KEY = "q"

func URLToCall(u *url.URL) (method string, args []string) {
	return path.Base(u.Path), u.Query()[ARG_URL_KEY]
}


func randomBytes() io.Reader {
	return bytes.NewBuffer([]byte(fmt.Sprintf("%x", rand.Int())))
}

func ComputeId(content io.Reader) (string, error) {
	h := sha256.New()
	if _, err := io.Copy(h, content); err != nil {
		return "", err
	}
	return fmt.Sprintf("%x", h.Sum(nil)[:8]), nil
}

func randomId() string {
	id, _ := ComputeId(randomBytes()) // can't fail
	return id
}


func humanDuration(d time.Duration) string {
	if seconds := int(d.Seconds()); seconds < 1 {
		return "Less than a second"
	} else if seconds < 60 {
		return fmt.Sprintf("%d seconds", seconds)
	} else if minutes := int(d.Minutes()); minutes == 1 {
		return "About a minute"
	} else if minutes < 60 {
		return fmt.Sprintf("%d minutes", minutes)
	} else if hours := int(d.Hours()); hours  == 1{
		return "About an hour"
	} else if hours < 48 {
		return fmt.Sprintf("%d hours", hours)
	} else if hours < 24 * 7 * 2 {
		return fmt.Sprintf("%d days", hours / 24)
	} else if hours < 24 * 30 * 3 {
		return fmt.Sprintf("%d weeks", hours / 24 / 7)
	} else if hours < 24 * 365 * 2 {
		return fmt.Sprintf("%d months", hours / 24 / 30)
	}
	return fmt.Sprintf("%d years", d.Hours() / 24 / 365)
}

func Subcmd(output io.Writer, name, signature, description string) *flag.FlagSet {
	flags := flag.NewFlagSet(name, flag.ContinueOnError)
	flags.SetOutput(output)
	flags.Usage = func() {
		fmt.Fprintf(output, "\nUsage: docker %s %s\n\n%s\n\n", name, signature, description)
		flags.PrintDefaults()
	}
	return flags
}

