import { Radio } from "@base-ui/react/radio"
import { RadioGroup } from "@base-ui/react/radio-group"
import { useSyncExternalStore } from "react"

import {
  ACCENTS,
  currentAccent,
  isAccent,
  setAccent,
  subscribeAccent,
} from "@/lib/accent"

export function AccentPicker() {
  const accent = useSyncExternalStore(subscribeAccent, currentAccent)
  return (
    <RadioGroup
      aria-label="Primary color"
      value={accent}
      onValueChange={(value) => {
        if (isAccent(value)) setAccent(value)
      }}
      className="flex h-9 shrink-0 items-center rounded-[10px] border border-input px-[3px]"
    >
      {ACCENTS.map((value) => {
        const label = `${value[0].toUpperCase()}${value.slice(1)} primary color`
        return (
          <Radio.Root
            key={value}
            value={value}
            aria-label={label}
            title={label}
            data-accent={value}
            nativeButton
            render={<button type="button" />}
            className="relative size-6 shrink-0 cursor-pointer rounded-full border-0 bg-transparent p-0 outline-offset-2 before:absolute before:inset-[3px] before:rounded-full before:bg-brand before:shadow-[inset_0_0_0_1px_#ffffff40] focus-visible:outline-2 focus-visible:outline-foreground data-checked:before:shadow-[0_0_0_2px_var(--background),0_0_0_4px_var(--brand)]"
          />
        )
      })}
    </RadioGroup>
  )
}
