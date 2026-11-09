"use client";
import React, { useState } from "react";
import { greet } from "./actions.js";

export function Interaction() {
  const [result, setResult] = useState("Ready");
  return (
    <section>
      <button
        onClick={async () => {
          try {
            setResult(await greet());
          } catch {
            setResult("Request failed. Try again.");
          }
        }}
      >
        Call server function
      </button>
      <p role="status">{result}</p>
    </section>
  );
}
