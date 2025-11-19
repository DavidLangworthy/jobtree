package cmd

import (
    "encoding/json"
    "fmt"
    "strings"
    "text/tabwriter"

    cobra "github.com/davidlangworthy/jobtree/cmd/kubectl-runs/internal/cobra"
)

// Printer renders output in table or json formats.
type Printer struct{}

// Payload describes the data to render.
type Payload struct {
    Headers []string
    Rows    [][]string
    Raw     interface{}
    Title   string
}

// Print renders the payload using the requested format.
func (p *Printer) Print(cmd *cobra.Command, opts *RootOptions, payload Payload) error {
    format := strings.ToLower(opts.Output)
    switch format {
    case "json":
        data := payload.Raw
        if data == nil {
            data = payload.Rows
        }
        enc, err := json.MarshalIndent(data, "", "  ")
        if err != nil {
            return err
        }
        fmt.Fprintln(cmd.OutOrStdout(), string(enc))
        return nil
    default:
        w := tabwriter.NewWriter(cmd.OutOrStdout(), 2, 4, 2, ' ', 0)
        if payload.Title != "" {
            fmt.Fprintln(w, payload.Title)
        }
        if len(payload.Headers) > 0 {
            fmt.Fprintln(w, strings.Join(payload.Headers, "\t"))
        }
        for _, row := range payload.Rows {
            fmt.Fprintln(w, strings.Join(row, "\t"))
        }
        return w.Flush()
    }
}
