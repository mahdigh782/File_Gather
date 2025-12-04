package main

import (
	"bytes"
	"fmt"
	"io"
	"io/ioutil"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"
)

// Tool: TreePicker Pro (TUI file selector)
//
// Behavior summary:
// - Left: directory tree (expand/collapse)
// - Navigation: hjkl (j/k down/up, h collapse/go to parent, l expand/go child)
// - Space: toggle selection (files only)
// - [ and ] : switch focus between left and right panes
// - Right pane: list of selected files
// - d (on right): remove file from selection
// - e (lowercase): write relative path + contents to a temporary file and either open in gedit or copy the concatenated content to the clipboard (user choice)
// - q: exit the application
// - Bottom helpbar shows key help (with colored background)
// - UI: overall colored border (blue), vertical separator between panes

func isDir(path string) bool {
	info, err := os.Stat(path)
	if err != nil {
		return false
	}
	return info.IsDir()
}

func sortedDirEntries(path string) ([]os.FileInfo, error) {
	entries, err := ioutil.ReadDir(path)
	if err != nil {
		return nil, err
	}
	sort.Slice(entries, func(i, j int) bool {
		a, b := entries[i], entries[j]
		if a.IsDir() && !b.IsDir() {
			return true
		}
		if !a.IsDir() && b.IsDir() {
			return false
		}
		return strings.ToLower(a.Name()) < strings.ToLower(b.Name())
	})
	return entries, nil
}

func main() {
	startDir, err := os.Getwd()
	if err != nil {
		log.Fatalf("error getting current directory: %v", err)
	}

	app := tview.NewApplication()

	// node maps for quick access and parent tracking
	nodeMap := make(map[string]*tview.TreeNode) // path -> node
	parentMap := make(map[string]string)        // childPath -> parentPath

	// selections
	selectedMap := make(map[string]struct{})
	var selectedList []string

	addSelected := func(path string) {
		if _, ok := selectedMap[path]; ok {
			return
		}
		selectedMap[path] = struct{}{}
		selectedList = append(selectedList, path)
	}

	removeSelected := func(path string) {
		if _, ok := selectedMap[path]; !ok {
			return
		}
		delete(selectedMap, path)
		newList := make([]string, 0, len(selectedList)-1)
		for _, p := range selectedList {
			if p != path {
				newList = append(newList, p)
			}
		}
		selectedList = newList
	}

	// selected list view (right)
	selectedListView := tview.NewList()
	selectedListView.ShowSecondaryText(false)
	refreshSelectedView := func() {
		selectedListView.Clear()
		for _, p := range selectedList {
			rel, _ := filepath.Rel(startDir, p)
			// show relative path + filename in the right pane
			selectedListView.AddItem(rel, "", 0, nil)
		}
	}

	// tree root
	rootNode := tview.NewTreeNode(startDir).
		SetReference(startDir).
		SetSelectable(true).
		SetColor(tcell.ColorYellow)
	nodeMap[startDir] = rootNode

	// lazy-load children
	var addChildren func(node *tview.TreeNode, path string)
	addChildren = func(node *tview.TreeNode, path string) {
		node.ClearChildren()
		entries, err := sortedDirEntries(path)
		if err != nil {
			// unable to read -> leave empty
			return
		}
		for _, e := range entries {
			childPath := filepath.Join(path, e.Name())
			var label string
			if e.IsDir() {
				label = fmt.Sprintf("[DIR] %s", e.Name())
			} else {
				label = e.Name()
			}
			child := tview.NewTreeNode(label).
				SetReference(childPath).
				SetSelectable(true)
			nodeMap[childPath] = child
			parentMap[childPath] = path
			if e.IsDir() {
				child.SetColor(tcell.ColorGreen)
				// placeholder to indicate expandable
				child.AddChild(tview.NewTreeNode("(loading)"))
			}
			node.AddChild(child)
		}
	}

	// initial load of root children
	addChildren(rootNode, startDir)

	tree := tview.NewTreeView().
		SetRoot(rootNode).
		SetCurrentNode(rootNode)

	// helper: move to parent based on parentMap
	moveToParent := func(cur *tview.TreeNode) {
		if cur == nil {
			return
		}
		ref := cur.GetReference()
		if ref == nil {
			return
		}
		path := ref.(string)
		parentPath, ok := parentMap[path]
		if !ok {
			// maybe parent is startDir or root; if path != startDir, try filepath.Dir
			if path == startDir {
				return
			}
			parentPath = filepath.Dir(path)
		}
		parentNode, ok := nodeMap[parentPath]
		if ok {
			tree.SetCurrentNode(parentNode)
		}
	}

	// helper: go to first child if exists
	moveToFirstChild := func(cur *tview.TreeNode) {
		if cur == nil {
			return
		}
		ref := cur.GetReference()
		if ref == nil {
			return
		}
		path := ref.(string)
		if !isDir(path) {
			return
		}
		// if placeholder present, load
		if len(cur.GetChildren()) == 1 && cur.GetChildren()[0].GetText() == "(loading)" {
			addChildren(cur, path)
		}
		children := cur.GetChildren()
		if len(children) > 0 {
			tree.SetCurrentNode(children[0])
		}
	}

	// SelectedFunc: Enter -> expand/collapse dir or toggle selection for file
	tree.SetSelectedFunc(func(node *tview.TreeNode) {
		if node == nil {
			return
		}
		ref := node.GetReference()
		if ref == nil {
			return
		}
		path := ref.(string)
		if isDir(path) {
			// lazy load if needed
			if len(node.GetChildren()) == 1 && node.GetChildren()[0].GetText() == "(loading)" {
				addChildren(node, path)
			}
			if node.IsExpanded() {
				node.Collapse()
			} else {
				node.Expand()
			}
		} else {
			// toggle file selection
			if _, ok := selectedMap[path]; ok {
				removeSelected(path)
			} else {
				addSelected(path)
			}
			refreshSelectedView()
		}
	})

	// Input capture for tree (hjkl, space, [, ], arrows, q)
	tree.SetInputCapture(func(event *tcell.EventKey) *tcell.EventKey {
		if event == nil {
			return event
		}
		// quick exit
		if event.Rune() == 'q' {
			app.Stop()
			return nil
		}
		switch event.Rune() {
		case 'j':
			// delegate to default handler for down key
			tree.InputHandler()(tcell.NewEventKey(tcell.KeyDown, 0, tcell.ModNone), nil)
			return nil
		case 'k':
			tree.InputHandler()(tcell.NewEventKey(tcell.KeyUp, 0, tcell.ModNone), nil)
			return nil
		case 'h':
			cur := tree.GetCurrentNode()
			if cur == nil {
				return nil
			}
			// if expanded -> collapse; otherwise move to parent
			if cur.IsExpanded() {
				cur.Collapse()
			} else {
				moveToParent(cur)
			}
			return nil
		case 'l':
			cur := tree.GetCurrentNode()
			if cur == nil {
				return nil
			}
			ref := cur.GetReference()
			if ref == nil {
				return nil
			}
			path := ref.(string)
			if isDir(path) {
				// lazy load then expand / go to first child
				if len(cur.GetChildren()) == 1 && cur.GetChildren()[0].GetText() == "(loading)" {
					addChildren(cur, path)
				}
				if !cur.IsExpanded() {
					cur.Expand()
				} else {
					moveToFirstChild(cur)
				}
			}
			return nil
		case ' ':
			// toggle selection if file
			cur := tree.GetCurrentNode()
			if cur == nil {
				return nil
			}
			ref := cur.GetReference()
			if ref == nil {
				return nil
			}
			path := ref.(string)
			if !isDir(path) {
				if _, ok := selectedMap[path]; ok {
					removeSelected(path)
				} else {
					addSelected(path)
				}
				refreshSelectedView()
			}
			return nil
		case '[':
			app.SetFocus(tree)
			return nil
		case ']':
			app.SetFocus(selectedListView)
			return nil
		}

		// handle arrow keys for expand/collapse fallback
		switch event.Key() {
		case tcell.KeyRight:
			cur := tree.GetCurrentNode()
			if cur == nil {
				return event
			}
			ref := cur.GetReference()
			if ref == nil {
				return event
			}
			path := ref.(string)
			if isDir(path) {
				if len(cur.GetChildren()) == 1 && cur.GetChildren()[0].GetText() == "(loading)" {
					addChildren(cur, path)
				}
				cur.Expand()
			}
			return nil
		case tcell.KeyLeft:
			cur := tree.GetCurrentNode()
			if cur == nil {
				return event
			}
			if cur.IsExpanded() {
				cur.Collapse()
			} else {
				moveToParent(cur)
			}
			return nil
		}

		return event
	})

	// behavior for selectedListView (right)
	selectedListView.SetDoneFunc(func() {
		app.SetFocus(tree)
	})
	selectedListView.SetInputCapture(func(event *tcell.EventKey) *tcell.EventKey {
		if event == nil {
			return event
		}
		// quick exit
		if event.Rune() == 'q' {
			app.Stop()
			return nil
		}
		switch event.Rune() {
		case 'j':
			selectedListView.InputHandler()(tcell.NewEventKey(tcell.KeyDown, 0, tcell.ModNone), nil)
			return nil
		case 'k':
			selectedListView.InputHandler()(tcell.NewEventKey(tcell.KeyUp, 0, tcell.ModNone), nil)
			return nil
		case 'd':
			// remove current item
			index := selectedListView.GetCurrentItem()
			if index < 0 || index >= len(selectedList) {
				return nil
			}
			path := selectedList[index]
			removeSelected(path)
			refreshSelectedView()
			// try to keep focus appropriate
			if index >= len(selectedList) && index > 0 {
				selectedListView.SetCurrentItem(index - 1)
			} else {
				selectedListView.SetCurrentItem(index)
			}
			return nil
		case '[':
			app.SetFocus(tree)
			return nil
		case ']':
			app.SetFocus(selectedListView)
			return nil
		case 'e':
			// Option: open in gedit OR copy to clipboard
			if len(selectedList) == 0 {
				modal := tview.NewModal().
					SetText("No files selected.").
					AddButtons([]string{"OK"}).
					SetDoneFunc(func(buttonIndex int, buttonLabel string) {
						app.SetRoot(frameWrapper, true).SetFocus(selectedListView)
					})
				app.SetRoot(modal, false)
				return nil
			}
			// present a choice modal
			choice := tview.NewModal().
				SetText("Open selected files in gedit or copy combined content to clipboard?").
				AddButtons([]string{"Gedit", "Copy to clipboard", "Cancel"}).
				SetDoneFunc(func(buttonIndex int, buttonLabel string) {
					app.SetRoot(frameWrapper, true).SetFocus(selectedListView)
					if buttonLabel == "Gedit" {
						// prepare buffer and temp file
						var buf bytes.Buffer
						for _, p := range selectedList {
							rel, err := filepath.Rel(startDir, p)
							if err != nil {
								rel = p
							}
							buf.WriteString(rel)
							buf.WriteString("\n\n")
							buf.WriteString("```\n")
							content, err := ioutil.ReadFile(p)
							if err != nil {
								buf.WriteString(fmt.Sprintf("error reading file: %v\n", err))
							} else {
								buf.Write(content)
								if len(content) == 0 || content[len(content)-1] != '\n' {
									buf.WriteString("\n")
								}
							}
							buf.WriteString("```\n\n")
						}
						tmp, err := ioutil.TempFile("", "treepicker_selected_*.txt")
						if err != nil {
							modalErr := tview.NewModal().
								SetText(fmt.Sprintf("Error creating temporary file: %v", err)).
								AddButtons([]string{"OK"}).
								SetDoneFunc(func(int, string) {
									app.SetRoot(frameWrapper, true).SetFocus(selectedListView)
								})
							app.SetRoot(modalErr, false)
							return
						}
						_, err = io.Copy(tmp, &buf)
						if err != nil {
							_ = tmp.Close()
							modalErr := tview.NewModal().
								SetText(fmt.Sprintf("Error writing temporary file: %v", err)).
								AddButtons([]string{"OK"}).
								SetDoneFunc(func(int, string) {
									app.SetRoot(frameWrapper, true).SetFocus(selectedListView)
								})
							app.SetRoot(modalErr, false)
							return
						}
						_ = tmp.Close()
						// try to open gedit
						cmd := exec.Command("gedit", tmp.Name())
						if err := cmd.Start(); err != nil {
							// fallback xdg-open
							fb := exec.Command("xdg-open", tmp.Name())
							if err2 := fb.Start(); err2 != nil {
								modalErr := tview.NewModal().
									SetText(fmt.Sprintf("Error opening editor: %v\n(fallback xdg-open also failed: %v)", err, err2)).
									AddButtons([]string{"OK"}).
									SetDoneFunc(func(int, string) {
										app.SetRoot(frameWrapper, true).SetFocus(selectedListView)
									})
								app.SetRoot(modalErr, false)
								return
							}
						}
						modalDone := tview.NewModal().
							SetText(fmt.Sprintf("Temporary file created: %s\n(Editor started)", tmp.Name())).
							AddButtons([]string{"OK"}).
							SetDoneFunc(func(int, string) {
								app.SetRoot(frameWrapper, true).SetFocus(selectedListView)
							})
						app.SetRoot(modalDone, false)
					} else if buttonLabel == "Copy to clipboard" {
						// prepare combined buffer
						var buf bytes.Buffer
						for _, p := range selectedList {
							rel, err := filepath.Rel(startDir, p)
							if err != nil {
								rel = p
							}
							buf.WriteString(rel)
							buf.WriteString("\n\n")
							content, err := ioutil.ReadFile(p)
							if err != nil {
								buf.WriteString(fmt.Sprintf("error reading file: %v\n", err))
							} else {
								buf.Write(content)
								if len(content) == 0 || content[len(content)-1] != '\n' {
									buf.WriteString("\n")
								}
							}
							buf.WriteString("\n---\n\n")
						}
						// try wl-copy then xclip
						copied := false
						if cmd := exec.Command("wl-copy"); cmd != nil {
							stdin, err := cmd.StdinPipe()
							if err == nil {
								if err := cmd.Start(); err == nil {
									_, _ = io.Copy(stdin, &buf)
									_ = stdin.Close()
									_ = cmd.Wait()
									copied = true
								}
							}
						}
						if !copied {
							// try xclip -selection clipboard
							buf2 := bytes.NewBuffer(buf.Bytes())
							cmd := exec.Command("xclip", "-selection", "clipboard")
							stdin, err := cmd.StdinPipe()
							if err == nil {
								if err := cmd.Start(); err == nil {
									_, _ = io.Copy(stdin, buf2)
									_ = stdin.Close()
									_ = cmd.Wait()
									copied = true
								}
							}
						}
						if !copied {
							modalErr := tview.NewModal().
								SetText("Failed to copy to clipboard: neither wl-copy nor xclip succeeded or were available.").
								AddButtons([]string{"OK"}).
								SetDoneFunc(func(int, string) {
									app.SetRoot(frameWrapper, true).SetFocus(selectedListView)
								})
							app.SetRoot(modalErr, false)
							return
						}
						modalDone := tview.NewModal().
							SetText("Combined content copied to clipboard.").
							AddButtons([]string{"OK"}).
							SetDoneFunc(func(int, string) {
								app.SetRoot(frameWrapper, true).SetFocus(selectedListView)
							})
						app.SetRoot(modalDone, false)
					}
				})
			app.SetRoot(choice, false)
			return nil
		}
		return event
	})

	// bottom help bar (with colored background)
	help := tview.NewTextView().
		SetDynamicColors(true).
		SetRegions(false).
		SetWrap(false)
	help.SetBackgroundColor(tcell.ColorBlue)
	fmt.Fprint(help, "Keys: h/j/k/l navigate  |  Space toggle selection  |  [ and ] change focus  |  d remove (right)  |  e open/copy  |  q quit")

	// Layout: body (left tree, separator, right list) + help bottom
	body := tview.NewFlex().SetDirection(tview.FlexColumn)

	// separator column: thin vertical colored bar
	separator := tview.NewBox()
	separator.SetBackgroundColor(tcell.ColorBlue)

	// give borders to left and right panes and titles
	tree.SetBorder(true).SetTitle(" Tree ")
	selectedListView.SetBorder(true).SetTitle(" Selected files ")

	// assemble body: tree | separator | selected list
	body.AddItem(tree, 0, 3, true)
	body.AddItem(separator, 1, 0, false) // thin vertical column
	body.AddItem(selectedListView, 0, 2, false)

	// wrap main layout in a frame with colored border and title (tool name)
	layout = tview.NewFlex().SetDirection(tview.FlexRow)
	layout.AddItem(body, 0, 1, true)
	layout.AddItem(help, 1, 1, false)

	frame := tview.NewFrame(layout)
	frame.SetBorders(0, 0, 0, 0, 0, 0)
	frame.SetBorder(true)
	frame.SetBorderColor(tcell.ColorBlue)
	frame.SetTitle(" File Gather ")

	frameWrapper = frame

	// initialize selected view
	refreshSelectedView()

	// run app
	if err := app.SetRoot(frameWrapper, true).SetFocus(tree).Run(); err != nil {
		log.Fatalf("error running application: %v", err)
	}
}

// package-level variables used by modal callbacks
var layout *tview.Flex
var frameWrapper *tview.Frame

