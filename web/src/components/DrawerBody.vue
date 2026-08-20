<template>
  <div class="db"><slot /></div>
</template>

<style scoped>
/*
 * 抽屉的滚动区。这里有两条容易踩空的规则，缺一个都会让卡片被压扁：
 *
 * 1. min-height: 0
 *    flex 子项默认 min-height: auto，会拒绝收缩到内容高度以下。缺了它，
 *    这个滚动区根本不会滚——内容整体溢出，把底部操作栏挤出视口。
 *
 * 2. grid-auto-rows: max-content
 *    SectionCard 带 overflow: hidden，而按 CSS Grid 规范，overflow 非 visible
 *    的网格项「自动最小尺寸」为 0——于是行高可以被无下限压缩。容器高度一旦
 *    确定（上一条让它确定了），四张卡片就会被均分成一样高的几十像素，
 *    内容再被自身的 overflow: hidden 裁掉。显式声明按内容定高才挡得住。
 */
.db {
  flex: 1 1 auto;
  min-height: 0;
  overflow-y: auto;
  padding: 20px;
  display: grid;
  align-content: start;
  grid-auto-rows: max-content;
  gap: 18px;
}
</style>
