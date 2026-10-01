# ADR 0007 – Eigene Tailwind-Komponenten statt shadcn-vue

Status: angenommen

Die UI nutzt Vue 3, Vite, TypeScript, Pinia, Tailwind (v4), ECharts und Vue Flow wie spezifiziert.
Statt der shadcn-vue-CLI werden wenige eigene, schlanke Komponenten (`web/src/components`) im gleichen
Stil verwendet – weniger generierter Code, volle Kontrolle über Barrierefreiheit (Status immer als
Icon + Text) und das Crimson/Void-Black-Design.
