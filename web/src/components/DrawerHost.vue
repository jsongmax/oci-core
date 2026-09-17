<script setup lang="ts">
import { useStore } from '@/store'
import InstanceDrawer from '@/views/drawers/InstanceDrawer.vue'
import AccountDrawer from '@/views/drawers/AccountDrawer.vue'
import AddAccountDrawer from '@/views/drawers/AddAccountDrawer.vue'
import CreateInstanceDrawer from '@/views/drawers/CreateInstanceDrawer.vue'

const { state } = useStore()
</script>

<template>
  <!-- key 必须带上 id。抽屉开着时仍能用 Ctrl+K 打开另一台实例/另一个账号，
       同类抽屉会被原地复用：标题换了，里面的详情、监控、挂载关系还是上一个的。
       实测过：确认框写着「分离 beta 的引导卷」，请求里带的却是 alpha 的挂载关系。 -->
  <template v-if="state.drawer">
    <InstanceDrawer v-if="state.drawer.kind === 'instance'" :key="`instance:${state.drawer.id}`"
                    :id="state.drawer.id" :tab="state.drawer.tab" />
    <AccountDrawer v-else-if="state.drawer.kind === 'account'" :key="`account:${state.drawer.id}`"
                   :id="state.drawer.id" :tab="state.drawer.tab" />
    <AddAccountDrawer v-else-if="state.drawer.kind === 'add-account'" />
    <CreateInstanceDrawer v-else-if="state.drawer.kind === 'create-instance'" />
  </template>
</template>
