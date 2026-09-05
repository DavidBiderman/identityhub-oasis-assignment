import * as React from "react";
import { Check, ChevronsUpDown, Search } from "lucide-react";
import { cn } from "@/lib/utils";

export interface ComboboxOption {
  value: string;
  label: string;
  hint?: string;
}

interface ComboboxProps {
  id: string;
  options: ComboboxOption[];
  value: string;
  onChange: (value: string) => void;
  placeholder?: string;
  emptyMessage?: string;
  loading?: boolean;
  disabled?: boolean;
  invalid?: boolean;
  describedBy?: string;
}

/**
 * A list you can pick from, or type to narrow.
 *
 * A plain select is unusable once an account has two hundred Jira projects, and
 * a free-text box invites a key that does not exist — which the API would then
 * have to reject after a round trip to the provider. This is both: every option
 * is reachable by scrolling, and typing filters them. What it never does is
 * accept a value that is not on the list.
 *
 * Filtering is a subsequence match rather than a substring one, so "nhf" finds
 * "NHI Findings" the way a fuzzy finder would. Options whose key matches sort
 * above ones where only the name does, because someone typing "NHI" wants the
 * NHI project first.
 */
export function Combobox({
  id,
  options,
  value,
  onChange,
  placeholder = "Select…",
  emptyMessage = "Nothing matches.",
  loading = false,
  disabled = false,
  invalid = false,
  describedBy,
}: ComboboxProps) {
  const [open, setOpen] = React.useState(false);
  const [search, setSearch] = React.useState("");
  const [active, setActive] = React.useState(0);

  const containerRef = React.useRef<HTMLDivElement>(null);
  const inputRef = React.useRef<HTMLInputElement>(null);
  const listRef = React.useRef<HTMLUListElement>(null);

  const selected = options.find((o) => o.value === value);
  const matches = React.useMemo(() => rank(options, search), [options, search]);

  // Keep the highlighted option in range as the list narrows, and in view as
  // the arrow keys move it.
  React.useEffect(() => setActive(0), [search]);
  React.useEffect(() => {
    if (open) listRef.current?.children[active]?.scrollIntoView({ block: "nearest" });
  }, [open, active]);

  // Closing on an outside click is what makes this feel like a select rather
  // than a panel someone has to dismiss.
  React.useEffect(() => {
    if (!open) return;
    function onPointerDown(event: PointerEvent) {
      if (!containerRef.current?.contains(event.target as Node)) close();
    }
    document.addEventListener("pointerdown", onPointerDown);
    return () => document.removeEventListener("pointerdown", onPointerDown);
  }, [open]);

  function close() {
    setOpen(false);
    setSearch("");
  }

  function choose(option: ComboboxOption) {
    onChange(option.value);
    close();
  }

  function onKeyDown(event: React.KeyboardEvent) {
    switch (event.key) {
      case "ArrowDown":
      case "ArrowUp": {
        event.preventDefault();
        if (!open) {
          setOpen(true);
          return;
        }
        const step = event.key === "ArrowDown" ? 1 : -1;
        setActive((i) => Math.min(Math.max(i + step, 0), matches.length - 1));
        return;
      }
      case "Enter":
        if (open && matches[active]) {
          event.preventDefault();
          choose(matches[active]);
        }
        return;
      case "Escape":
        if (open) {
          event.preventDefault();
          close();
        }
        return;
      case "Tab":
        close();
    }
  }

  const listboxId = `${id}-listbox`;

  return (
    <div ref={containerRef} className="relative">
      <button
        type="button"
        id={id}
        role="combobox"
        aria-expanded={open}
        aria-controls={listboxId}
        aria-haspopup="listbox"
        aria-invalid={invalid || undefined}
        aria-describedby={describedBy}
        disabled={disabled || loading}
        onClick={() => {
          setOpen((wasOpen) => !wasOpen);
          // Focus lands in the search box, so typing filters immediately.
          requestAnimationFrame(() => inputRef.current?.focus());
        }}
        onKeyDown={onKeyDown}
        className={cn(
          "flex h-9 w-full items-center justify-between gap-2 rounded-md border",
          "border-line bg-surface px-3 text-left text-sm text-ink",
          "disabled:opacity-60 aria-[invalid=true]:border-danger",
        )}
      >
        <span className={cn("truncate", !selected && "text-muted")}>
          {loading ? "Loading…" : (selected?.label ?? placeholder)}
        </span>
        <ChevronsUpDown className="size-4 shrink-0 text-muted" aria-hidden />
      </button>

      {open && (
        <div
          className={cn(
            "absolute z-20 mt-1 w-full overflow-hidden rounded-md border",
            "border-line bg-surface shadow-lg",
          )}
        >
          <div className="flex items-center gap-2 border-b border-line px-3">
            <Search className="size-4 shrink-0 text-muted" aria-hidden />
            <input
              ref={inputRef}
              value={search}
              onChange={(e) => setSearch(e.target.value)}
              onKeyDown={onKeyDown}
              placeholder="Type to filter…"
              aria-controls={listboxId}
              aria-autocomplete="list"
              className="h-9 w-full bg-transparent text-sm text-ink outline-none placeholder:text-muted"
            />
          </div>

          <ul id={listboxId} ref={listRef} role="listbox" className="max-h-64 overflow-y-auto py-1">
            {matches.length === 0 && (
              <li className="px-3 py-2 text-sm text-muted">{emptyMessage}</li>
            )}
            {matches.map((option, i) => (
              <li
                key={option.value}
                role="option"
                aria-selected={option.value === value}
                onPointerEnter={() => setActive(i)}
                onClick={() => choose(option)}
                className={cn(
                  "flex cursor-pointer items-center gap-2 px-3 py-1.5 text-sm",
                  i === active && "bg-accent/10",
                )}
              >
                <Check
                  className={cn(
                    "size-4 shrink-0 text-accent",
                    option.value !== value && "invisible",
                  )}
                  aria-hidden
                />
                <span className="truncate text-ink">{option.label}</span>
                {option.hint && (
                  <span className="ml-auto shrink-0 truncate text-xs text-muted">
                    {option.hint}
                  </span>
                )}
              </li>
            ))}
          </ul>
        </div>
      )}
    </div>
  );
}

/**
 * rank filters and orders options against what has been typed.
 *
 * A subsequence match, so "nhf" finds "NHI Findings". Matches earlier in the
 * string score higher, and a match on the value outranks one on the label:
 * someone typing "NHI" is naming a project key, not describing it.
 */
function rank(options: ComboboxOption[], search: string): ComboboxOption[] {
  const needle = search.trim().toLowerCase();
  if (needle === "") return options;

  return options
    .map((option) => ({
      option,
      score: Math.max(
        score(option.value.toLowerCase(), needle) * 2,
        score(option.label.toLowerCase(), needle),
      ),
    }))
    .filter((m) => m.score > 0)
    .sort((a, b) => b.score - a.score)
    .map((m) => m.option);
}

/** score returns 0 when needle is not a subsequence of haystack. */
function score(haystack: string, needle: string): number {
  let at = 0;
  let total = 0;

  for (const ch of needle) {
    const found = haystack.indexOf(ch, at);
    if (found === -1) return 0;
    // A character right after the previous one is worth more than one found
    // far away, so a prefix beats a scattered match.
    total += found === at ? 3 : 1;
    at = found + 1;
  }
  return total;
}
