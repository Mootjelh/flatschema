# flatschema

[![CI](https://github.com/Mootjelh/flatschema/actions/workflows/ci.yml/badge.svg)](https://github.com/Mootjelh/flatschema/actions/workflows/ci.yml)
[![Go Reference](https://pkg.go.dev/badge/github.com/Mootjelh/flatschema.svg)](https://pkg.go.dev/github.com/Mootjelh/flatschema)

Infer a FlatBuffers schema from sample buffers.

One dependency, [flatread](https://github.com/Mootjelh/flatread), which does the reading.

## Why

FlatBuffers data carries no schema. A buffer is a vtable of slot offsets and the bytes they point at; the names, the types and the widths live only in the `.fbs` that generated it. When you have the data and not the file, the usual route is to read the bytes by hand, write down which slot held what, and build the schema up one field at a time.

This does that pass for you.

```
$ flatschema payload.bin
// Inferred by flatschema from 1 sample buffer(s).
//
// Names are not in the data, so fields are named for their slot and types
// for the route taken to reach them. Scalar widths are not in the data
// either; every scalar below is a guess and the comment beside it shows
// what was actually there. Rename and retype freely, but see the note on
// ids before deleting anything.

// Seen 1 time(s).
table Root {
  f4: uint (id: 0);                 // width unknown; u32=4242 u16=4242 u8=146
  f6: string (id: 1);               // "hello"
  f8: [ubyte] (id: 2);              // 3 bytes
  f10: T10 (id: 3);
  f12: [T12] (id: 4);               // 2 elements
}
...
root_type Root;
```

## Install

```bash
go install github.com/Mootjelh/flatschema/cmd/flatschema@latest
```

## Give it more than one buffer

A slot is only visible when some buffer populates it. One sample yields the fields that one sample used, and nothing else:

```bash
flatschema *.bin > schema.fbs
```

Merging is the point. A field only the third buffer sets shows up, and the comment says `present in 1 of 6`, which tells you it is optional far more reliably than any guess from a single sample would.

Where samples disagree about a slot's type, the output says so rather than picking quietly:

```
f6: string (id: 1);   // samples disagreed on the type: string x2, bytes x2
```

## The gaps matter more than they look

A field's slot comes from its position in the table. Leave out a slot no sample happened to populate and every field after it shifts down by one, so the schema then reads the wrong field for the rest of the table. It compiles. It is wrong everywhere.

So unpopulated slots get a placeholder, and every field carries an explicit id:

```
f4: uint (id: 0);
unused6: ubyte (id: 1);           // no sample populated this slot
unused8: ubyte (id: 2);           // no sample populated this slot
f10: T10 (id: 3);
```

Keep the ids when you edit. They are what makes a later deletion fail at compile time instead of silently moving four fields.

## What it cannot recover, and why

* **Names.** They are not in the buffer in any form. Fields come out as `f4`, `f6`, and types as `Root`, `T10`, `T12_4`, named for the route taken to reach them.
* **Scalar widths.** Four bytes in a slot is equally a `uint`, an `int`, a `float`, or the first four of something wider. Scalars come out as `uint` and the comment shows the value read at every width, because recognising the number is usually how you recognise the field.
* **Structs.** A vector of structs and a vector of bytes are stored identically, so both read as `[ubyte]`. `flatread` has the same limit and documents it; if you know the element size, its `-struct` flag will show you the records.

The output is a skeleton to edit, not a recovered original. What it saves you is the mechanical half.

## What has been checked

`flatc` compiles the output, and CI keeps it that way. The `flatc` job builds sample buffers, renders the schema and compiles it with `flatc 25.12.19` on every push, over the shapes that have their own branch in the renderer: scalars, gaps between slots, samples that disagree, a vector of tables, a vector of tables the walk stopped before, a table with no fields, and all of them merged.

Two things about that job are deliberate. It sets `FLATSCHEMA_REQUIRE_FLATC`, because the test skips when `flatc` is missing and a skipped check is a green tick that means nothing. And it compiles a schema that `flatc` has to reject, since a check that passes everything would pass this too.

Running `flatc` for the first time found a bug, which is the argument for having done it. A vector of tables was written as `[ubyte]` when the walk stopped before reaching its elements, and a vector of `[ubyte]` is `[[ubyte]]`. FlatBuffers has no nested vectors:

```
error: nested vector types not supported (wrap in table first)
```

The default depth limit is 12, so a buffer nested twelve deep with a vector of tables at the cut point was enough to produce a schema that would not compile, with no flags involved. Those types are declared as empty tables now ([#1](https://github.com/Mootjelh/flatschema/issues/1)).

Beyond CI, the output was compiled for 198 combinations of real buffers and depth limits, including buffers `flatc` itself generated from a known schema so the inference could be checked against the file it came from.

If you hit a schema `flatc` rejects, that is a bug worth an issue, and the buffer that produced it is the useful part.

## As a library

```go
schema, err := flatschema.Infer(bufA, bufB, bufC)
if err != nil {
    return err
}
fmt.Print(schema.FBS())
```

`Schema.Types` is the inferred shape if you want to do something other than print it: each `Type` carries the slot path that identifies it, how many tables of that shape were seen, and per field the kinds observed and how often.

## What it doesn't do

* No encoding. This reads buffers and writes a schema.
* No union detection. `flatread` has a `UnionHint` for the shape; it is not wired in here yet.
* No name recovery from generated code. If you have the generated accessors, they carry the real names and are a better source than any inference.

## License

MIT
