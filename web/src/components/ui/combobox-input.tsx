/*
Copyright (C) 2023-2026 QuantumNous

This program is free software: you can redistribute it and/or modify
it under the terms of the GNU Affero General Public License as
published by the Free Software Foundation, either version 3 of the
License, or (at your option) any later version.

This program is distributed in the hope that it will be useful,
but WITHOUT ANY WARRANTY; without even the implied warranty of
MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
GNU Affero General Public License for more details.

You should have received a copy of the GNU Affero General Public License
along with this program. If not, see <https://www.gnu.org/licenses/>.

For commercial licensing, please contact support@quantumnous.com
*/
import { Check, ChevronsUpDown } from 'lucide-react'
import * as React from 'react'
import { createPortal } from 'react-dom'
import { useTranslation } from 'react-i18next'

import { Input } from '@/components/ui/input'
import { cn } from '@/lib/utils'

export type ComboboxInputOption = {
  value: string
  label: string
  icon?: React.ReactNode
  disabled?: boolean
  description?: string
  // APIDUO:mcp-category-group:BEGIN
  /**
   * 分组标题。相同 group 的选项必须相邻 —— 这里不做排序归并，
   * 顺序由调用方决定（它才知道业务上哪个组该在前）。
   */
  group?: string
  // APIDUO:mcp-category-group:END
}

interface ComboboxInputProps {
  options: readonly ComboboxInputOption[]
  value?: string
  onValueChange: (value: string) => void
  placeholder?: string
  emptyText?: string
  className?: string
  popupClassName?: string
  id?: string
  allowCustomValue?: boolean
  openOnFocus?: boolean
  disabled?: boolean
  onKeyDown?: React.KeyboardEventHandler<HTMLInputElement>
  'aria-label'?: string
  'aria-labelledby'?: string
  'aria-invalid'?: React.AriaAttributes['aria-invalid']
}

export function ComboboxInput({
  options,
  value = '',
  onValueChange,
  placeholder = 'Select or type...',
  emptyText = 'No option found.',
  className,
  popupClassName,
  id,
  allowCustomValue = false,
  openOnFocus = true,
  disabled = false,
  onKeyDown,
  'aria-label': ariaLabel,
  'aria-labelledby': ariaLabelledBy,
  'aria-invalid': ariaInvalid,
}: ComboboxInputProps) {
  const { t } = useTranslation()
  const listId = React.useId()
  const [open, setOpen] = React.useState(false)
  const [searchValue, setSearchValue] = React.useState('')
  const [searchChanged, setSearchChanged] = React.useState(false)
  const [highlightedIndex, setHighlightedIndex] = React.useState(-1)
  const containerRef = React.useRef<HTMLDivElement>(null)
  const inputRef = React.useRef<HTMLInputElement>(null)
  const listRef = React.useRef<HTMLUListElement>(null)
  const dropdownRef = React.useRef<HTMLDivElement>(null)
  // The dropdown is portaled so scrolling ancestors cannot clip it. It goes
  // into the enclosing dialog when there is one, which keeps a modal parent
  // from treating option clicks as outside presses.
  const [dropdown, setDropdown] = React.useState<{
    container: HTMLElement
    top: number
    left: number
    width: number
  } | null>(null)
  const pointerFocusRef = React.useRef(false)
  const selectedOption = React.useMemo(
    () => options.find((option) => option.value === value),
    [options, value]
  )
  const displayValue = open ? searchValue : (selectedOption?.label ?? value)

  const filteredOptions = React.useMemo(() => {
    if (!searchChanged || !searchValue.trim()) return options
    const search = searchValue.toLowerCase().trim()
    return options.filter(
      (option) =>
        option.label.toLowerCase().includes(search) ||
        option.value.toLowerCase().includes(search)
    )
  }, [options, searchValue, searchChanged])

  // Reset highlight when filtered options change
  React.useEffect(() => {
    setHighlightedIndex(-1)
  }, [filteredOptions])

  // Handle click outside to close
  React.useEffect(() => {
    if (!open) return

    const handleClickOutside = (e: MouseEvent) => {
      const target = e.target as Node
      if (
        containerRef.current &&
        !containerRef.current.contains(target) &&
        !dropdownRef.current?.contains(target)
      ) {
        setOpen(false)
        setSearchValue('')
      }
    }

    document.addEventListener('mousedown', handleClickOutside)
    return () => document.removeEventListener('mousedown', handleClickOutside)
  }, [open])

  // Position the portaled dropdown under the input and follow scroll/resize.
  React.useEffect(() => {
    if (!open) {
      setDropdown(null)
      return
    }
    const input = inputRef.current
    if (!input) return
    const container =
      input.closest<HTMLElement>('[role="dialog"]') ?? document.body
    const measure = () => {
      const rect = input.getBoundingClientRect()
      setDropdown({
        container,
        top: rect.bottom + 4,
        left: rect.left,
        width: rect.width,
      })
    }
    measure()
    window.addEventListener('scroll', measure, true)
    window.addEventListener('resize', measure)
    return () => {
      window.removeEventListener('scroll', measure, true)
      window.removeEventListener('resize', measure)
    }
  }, [open])

  const handleSelect = (selectedValue: string) => {
    onValueChange(selectedValue)
    setOpen(false)
    setSearchValue('')
    inputRef.current?.focus()
  }

  const handleKeyDown = (e: React.KeyboardEvent<HTMLInputElement>) => {
    if (!open && (e.key === 'ArrowDown' || e.key === 'ArrowUp')) {
      e.preventDefault()
      setSearchValue(allowCustomValue ? value : '')
      setSearchChanged(false)
      setOpen(true)
      return
    }

    if (!open) return

    switch (e.key) {
      case 'ArrowDown':
        e.preventDefault()
        setHighlightedIndex((prev) =>
          prev < filteredOptions.length - 1 ? prev + 1 : 0
        )
        break
      case 'ArrowUp':
        e.preventDefault()
        setHighlightedIndex((prev) =>
          prev > 0 ? prev - 1 : filteredOptions.length - 1
        )
        break
      case 'Enter':
        if (highlightedIndex >= 0 && filteredOptions[highlightedIndex]) {
          e.preventDefault()
          handleSelect(filteredOptions[highlightedIndex].value)
        } else if (allowCustomValue && searchValue.trim()) {
          e.preventDefault()
          handleSelect(searchValue.trim())
        } else {
          if (!onKeyDown) e.preventDefault()
          // No highlighted option, just close the dropdown and keep current value
          setOpen(false)
          setSearchValue('')
        }
        break
      case 'Escape':
        e.preventDefault()
        e.stopPropagation()
        setOpen(false)
        setSearchValue('')
        break
    }
  }

  // Scroll highlighted item into view
  React.useEffect(() => {
    if (highlightedIndex < 0 || !listRef.current) return
    // APIDUO:mcp-category-group:BEGIN
    // 按属性定位而不是 children[index]：列表里会插分组标题（role=presentation），
    // 用下标取节点会在第一个标题之后整体错位一格。
    const item = listRef.current.querySelector<HTMLElement>(
      '[data-highlighted="true"]'
    )
    // APIDUO:mcp-category-group:END
    item?.scrollIntoView({ block: 'nearest' })
  }, [highlightedIndex])

  const showDropdown =
    open &&
    !disabled &&
    (filteredOptions.length > 0 || (allowCustomValue && searchValue.trim()))

  return (
    <div ref={containerRef} className='relative'>
      <Input
        ref={inputRef}
        id={id}
        type='text'
        role='combobox'
        disabled={disabled}
        aria-label={ariaLabel}
        aria-labelledby={ariaLabelledBy}
        aria-invalid={ariaInvalid}
        aria-expanded={!!showDropdown}
        aria-controls={
          showDropdown && filteredOptions.length > 0 ? listId : undefined
        }
        aria-activedescendant={
          showDropdown && highlightedIndex >= 0
            ? `${listId}-${highlightedIndex}`
            : undefined
        }
        aria-haspopup='listbox'
        aria-autocomplete='list'
        autoComplete='off'
        placeholder={placeholder}
        value={displayValue}
        onChange={(e) => {
          const nextValue = e.target.value
          setSearchValue(nextValue)
          setSearchChanged(true)
          if (allowCustomValue) {
            onValueChange(nextValue)
          }
          if (!open) setOpen(true)
        }}
        onPointerDown={() => {
          pointerFocusRef.current = true
          if (document.activeElement === inputRef.current && !open) {
            setSearchValue(allowCustomValue ? value : '')
            setSearchChanged(false)
            setOpen(true)
          }
        }}
        onFocus={() => {
          setSearchValue(allowCustomValue ? value : '')
          setSearchChanged(false)
          if (openOnFocus || pointerFocusRef.current) {
            setOpen(true)
          }
          pointerFocusRef.current = false
        }}
        onBlur={() => {
          setOpen(false)
          setSearchValue('')
        }}
        onKeyDown={(event) => {
          handleKeyDown(event)
          if (!event.defaultPrevented) onKeyDown?.(event)
        }}
        className={cn('pr-9', className)}
      />
      <ChevronsUpDown className='pointer-events-none absolute top-1/2 right-3 size-4 shrink-0 -translate-y-1/2 opacity-50' />

      {showDropdown &&
        dropdown &&
        createPortal(
          <div
            ref={dropdownRef}
            style={{
              position: 'fixed',
              top: dropdown.top,
              left: dropdown.left,
              width: dropdown.width,
            }}
            className={cn(
              'bg-popover text-popover-foreground z-100 rounded-md border shadow-md',
              popupClassName
            )}
          >
            {filteredOptions.length > 0 ? (
              <ul
                ref={listRef}
                id={listId}
                role='listbox'
                className='max-h-[200px] overflow-y-auto p-1'
              >
                {/* APIDUO:mcp-category-group:BEGIN */}
                {filteredOptions.map((option, index) => {
                  const prev = filteredOptions[index - 1]
                  const showGroup =
                    !!option.group && option.group !== prev?.group
                  return (
                    <React.Fragment key={option.value}>
                      {showGroup ? (
                        // role=presentation + aria-hidden：只是视觉分节，
                        // 屏幕阅读器仍按 option 顺序读，键盘索引也不受影响。
                        <li
                          role='presentation'
                          aria-hidden='true'
                          className='text-muted-foreground px-2 pt-2 pb-1 text-[10px] font-medium tracking-wide'
                        >
                          {option.group}
                        </li>
                      ) : null}
                      <li
                        id={`${listId}-${index}`}
                        role='option'
                        aria-selected={value === option.value}
                        data-highlighted={index === highlightedIndex}
                        className={cn(
                          'relative flex cursor-pointer items-center gap-2 rounded-sm px-2 py-1.5 text-sm select-none',
                          index === highlightedIndex &&
                            'bg-accent text-accent-foreground',
                          value === option.value && 'font-medium'
                        )}
                        onMouseEnter={() => setHighlightedIndex(index)}
                        onMouseDown={(e) => {
                          e.preventDefault() // Prevent blur
                          handleSelect(option.value)
                        }}
                      >
                        <Check
                          className={cn(
                            'size-4 shrink-0',
                            value === option.value ? 'opacity-100' : 'opacity-0'
                          )}
                        />
                        {option.icon && <span aria-hidden>{option.icon}</span>}
                        <span className='truncate'>{option.label}</span>
                      </li>
                    </React.Fragment>
                  )
                })}
                {/* APIDUO:mcp-category-group:END */}
              </ul>
            ) : (
              <div className='px-2 py-6 text-center text-sm'>
                {t(emptyText)}
                {allowCustomValue && searchValue.trim() && (
                  <div className='text-muted-foreground mt-1 text-xs'>
                    {t('Press Enter to use "{{value}}"', {
                      value: searchValue.trim(),
                    })}
                  </div>
                )}
              </div>
            )}
          </div>,
          dropdown.container
        )}
    </div>
  )
}
