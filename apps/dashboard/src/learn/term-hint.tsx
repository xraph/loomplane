// A small "?" beside a word the app uses in its own sense, which opens a
// one-paragraph explanation and a lesson that goes further.
import { useState } from "react"

import { Button } from "@/components/ui/button"
import {
  Popover,
  PopoverContent,
  PopoverDescription,
  PopoverTitle,
  PopoverTrigger,
} from "@/components/ui/popover"
import { Link } from "@/lib/router"

import type { Concept } from "./content"

export function TermHint({ term }: { term: Concept }) {
  const [open, setOpen] = useState(false)
  return (
    <Popover open={open} onOpenChange={setOpen}>
      <PopoverTrigger
        render={
          <button
            type="button"
            aria-label={`What does ${term.word} mean?`}
            className="inline-flex size-4 items-center justify-center rounded-full bg-muted align-middle text-[10px] font-bold text-muted-foreground hover:bg-primary hover:text-primary-foreground focus-visible:ring-2 focus-visible:ring-ring focus-visible:outline-none"
          />
        }
      >
        ?
      </PopoverTrigger>
      <PopoverContent align="start" className="w-[340px] gap-2">
        <PopoverTitle className="text-[15px] font-semibold">
          {term.word}
        </PopoverTitle>
        <PopoverDescription className="text-[13px] leading-relaxed">
          {term.body}
        </PopoverDescription>
        <div className="flex justify-between gap-2 pt-1">
          <Button
            variant="ghost"
            size="sm"
            className="-ml-2 text-primary"
            nativeButton={false}
            render={<Link to="/learn" />}
          >
            More in Learn
          </Button>
          <Button variant="ghost" size="sm" onClick={() => setOpen(false)}>
            Got it
          </Button>
        </div>
      </PopoverContent>
    </Popover>
  )
}
