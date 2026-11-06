package ytparse

// Version names the parsers' current behaviour. It is stored with every raw
// response (REQ-010 fix 8) and will go into each event's provenance
// (parser_version), so a record can be traced to the parsers that read it.
// Change it whenever a parser's output changes: the date of the change,
// then a counter for the day.
const Version = "2026.11.06.1"
